package mock

import (
	"bytes"
	"encoding/xml"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pgodlews/verity-api/core/verr"
)

const (
	soap11NS = "http://schemas.xmlsoap.org/wsdl/soap/"
	soap12NS = "http://schemas.xmlsoap.org/wsdl/soap12/"
	env11NS  = "http://schemas.xmlsoap.org/soap/envelope/"
	env12NS  = "http://www.w3.org/2003/05/soap-envelope"
)

// wsdlDoc is the subset of WSDL 1.1 and inline XSD the mock server needs.
type wsdlDoc struct {
	defs     wsdlDefinitions
	prefixes map[string]string
	elem     map[string]xsdElement
	elemName map[string]xsdElement
	complex  map[string]xsdComplex
	simple   map[string]xsdSimple
	messages map[string]wsdlMessage
	ops      []wsdlOp
}

type wsdlOp struct {
	name     string
	action   string
	version  string
	tns      string
	inNS     string
	inLocal  string
	outLocal string
	outMsg   string
}

type wsdlDefinitions struct {
	TargetNamespace string         `xml:"targetNamespace,attr"`
	Types           wsdlTypes      `xml:"types"`
	Messages        []wsdlMessage  `xml:"message"`
	PortTypes       []wsdlPortType `xml:"portType"`
	Bindings        []wsdlBinding  `xml:"binding"`
}

type wsdlTypes struct {
	Schemas []xsdSchema `xml:"schema"`
}

type xsdSchema struct {
	TargetNS     string       `xml:"targetNamespace,attr"`
	Elements     []xsdElement `xml:"element"`
	ComplexTypes []xsdComplex `xml:"complexType"`
	SimpleTypes  []xsdSimple  `xml:"simpleType"`
}

type xsdElement struct {
	Name      string      `xml:"name,attr"`
	Type      string      `xml:"type,attr"`
	Ref       string      `xml:"ref,attr"`
	MinOccurs string      `xml:"minOccurs,attr"`
	MaxOccurs string      `xml:"maxOccurs,attr"`
	Complex   *xsdComplex `xml:"complexType"`
	Simple    *xsdSimple  `xml:"simpleType"`
	NS        string      `xml:"-"`
}

type xsdComplex struct {
	Name           string    `xml:"name,attr"`
	Sequence       *xsdGroup `xml:"sequence"`
	All            *xsdGroup `xml:"all"`
	Choice         *xsdGroup `xml:"choice"`
	ComplexContent struct {
		Extension *xsdExtension `xml:"extension"`
	} `xml:"complexContent"`
	SimpleContent struct {
		Extension *xsdExtension `xml:"extension"`
	} `xml:"simpleContent"`
}

type xsdGroup struct {
	Elements []xsdElement `xml:"element"`
}

type xsdExtension struct {
	Base     string    `xml:"base,attr"`
	Sequence *xsdGroup `xml:"sequence"`
}

type xsdSimple struct {
	Name        string          `xml:"name,attr"`
	Restriction *xsdRestriction `xml:"restriction"`
}

type xsdFacet struct {
	Value string `xml:"value,attr"`
}

type xsdRestriction struct {
	Base  string     `xml:"base,attr"`
	Enums []xsdFacet `xml:"enumeration"`
}

type wsdlMessage struct {
	Name  string     `xml:"name,attr"`
	Parts []wsdlPart `xml:"part"`
}

type wsdlPart struct {
	Name    string `xml:"name,attr"`
	Element string `xml:"element,attr"`
	Type    string `xml:"type,attr"`
}

type wsdlPortType struct {
	Name       string           `xml:"name,attr"`
	Operations []wsdlPortTypeOp `xml:"operation"`
}

type wsdlPortTypeOp struct {
	Name  string `xml:"name,attr"`
	Input struct {
		Message string `xml:"message,attr"`
	} `xml:"input"`
	Output struct {
		Message string `xml:"message,attr"`
	} `xml:"output"`
}

type wsdlBinding struct {
	Name       string          `xml:"name,attr"`
	Operations []wsdlBindingOp `xml:"operation"`
}

type wsdlBindingOp struct {
	Name      string       `xml:"name,attr"`
	Operation wsdlSOAPOpEl `xml:"operation"`
}

type wsdlSOAPOpEl struct {
	SOAPAction string `xml:"soapAction,attr"`
}

type readyOp struct {
	name    string
	action  string
	version string
	inNS    string
	inLocal string
	body    string
	content string
}

type soapHandler struct {
	ops      []readyOp
	delay    time.Duration
	fallback string
}

func newWSDL(data []byte, opts Options) (http.Handler, error) {
	doc, err := parseWSDL(data)
	if err != nil {
		return nil, err
	}
	if len(doc.ops) == 0 {
		return nil, verr.New(verr.CodeConfig, "WSDL contained no operations")
	}
	ops := make([]readyOp, len(doc.ops))
	only12 := true
	for i, op := range doc.ops {
		ver := op.version
		if ver == "" {
			ver = "1.1"
		}
		if ver != "1.2" {
			only12 = false
		}
		ops[i] = readyOp{
			name:    op.name,
			action:  op.action,
			version: ver,
			inNS:    op.inNS,
			inLocal: op.inLocal,
			body:    soapEnvelope(ver, doc.responseInner(op, opts.Seed)),
			content: soapContentType(ver),
		}
	}
	fallback := "1.1"
	if only12 {
		fallback = "1.2"
	}
	return &soapHandler{ops: ops, delay: opts.Delay, fallback: fallback}, nil
}

func (s *soapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := readLimited(r)
	if err != nil {
		writeProblem(w, http.StatusRequestEntityTooLarge, "Payload Too Large", err.Error(), nil)
		return
	}
	pause(r, s.delay)
	if r.Context().Err() != nil {
		return
	}
	action := soapActionOf(r)
	_, local, ns := firstBodyElement(body)
	op := s.match(action, ns, local, s.versionFor(r))
	if op == nil {
		ver := s.versionFor(r)
		w.Header().Set("Content-Type", soapContentType(ver))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(soapEnvelope(ver, soapFault(ver, "unknown operation"))))
		return
	}
	w.Header().Set("Content-Type", op.content)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(op.body))
}

func (s *soapHandler) match(action, ns, local, ver string) *readyOp {
	if action != "" {
		var fallback *readyOp
		for i := range s.ops {
			if s.ops[i].action != action {
				continue
			}
			if ver != "" && s.ops[i].version == ver {
				return &s.ops[i]
			}
			if fallback == nil {
				fallback = &s.ops[i]
			}
		}
		return fallback
	}
	var fallback *readyOp
	for i := range s.ops {
		op := &s.ops[i]
		hit := op.inLocal != "" && op.inLocal == local && (op.inNS == "" || op.inNS == ns)
		if !hit && local != "" && (local == op.name || local == op.name+"Request") {
			hit = true
		}
		if !hit {
			continue
		}
		if ver != "" && op.version == ver {
			return op
		}
		if fallback == nil {
			fallback = op
		}
	}
	return fallback
}

func (s *soapHandler) versionFor(r *http.Request) string {
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.Contains(ct, "application/soap+xml") {
		return "1.2"
	}
	if strings.Contains(ct, "text/xml") {
		return "1.1"
	}
	return s.fallback
}

func soapActionOf(r *http.Request) string {
	if a := strings.TrimSpace(r.Header.Get("SOAPAction")); a != "" {
		return strings.Trim(a, `"`)
	}
	ct := r.Header.Get("Content-Type")
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	return strings.Trim(params["action"], `"`)
}

func soapContentType(v string) string {
	if v == "1.2" {
		return "application/soap+xml; charset=utf-8"
	}
	return "text/xml; charset=utf-8"
}

func soapEnvelope(version, inner string) string {
	ns := env11NS
	if version == "1.2" {
		ns = env12NS
	}
	return `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<soap:Envelope xmlns:soap="` + ns + `"><soap:Body>` + inner + `</soap:Body></soap:Envelope>`
}

func soapFault(version, msg string) string {
	msg = xmlEscape(msg)
	if version == "1.2" {
		return `<soap:Fault><soap:Code><soap:Value>soap:Sender</soap:Value></soap:Code>` +
			`<soap:Reason><soap:Text xml:lang="en">` + msg + `</soap:Text></soap:Reason></soap:Fault>`
	}
	return `<soap:Fault><faultcode>soap:Client</faultcode><faultstring>` + msg + `</faultstring></soap:Fault>`
}

func firstBodyElement(data []byte) (ok bool, local, ns string) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	inBody := false
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return false, "", ""
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !inBody && t.Name.Local == "Body" {
				inBody = true
				depth = 1
				continue
			}
			if inBody && depth == 1 {
				return true, t.Name.Local, t.Name.Space
			}
			if inBody {
				depth++
			}
		case xml.EndElement:
			if inBody {
				depth--
				if depth == 0 {
					return false, "", ""
				}
			}
		}
	}
}

func parseWSDL(data []byte) (*wsdlDoc, error) {
	var def wsdlDefinitions
	if err := xml.Unmarshal(data, &def); err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "parse WSDL")
	}
	doc := &wsdlDoc{
		defs:     def,
		prefixes: xmlnsMap(data),
		elem:     map[string]xsdElement{},
		elemName: map[string]xsdElement{},
		complex:  map[string]xsdComplex{},
		simple:   map[string]xsdSimple{},
		messages: map[string]wsdlMessage{},
	}
	for _, schema := range def.Types.Schemas {
		ns := schema.TargetNS
		if ns == "" {
			ns = def.TargetNamespace
		}
		for _, el := range schema.Elements {
			el.NS = ns
			doc.elem[ns+" "+el.Name] = el
			doc.elemName[el.Name] = el
		}
		for _, c := range schema.ComplexTypes {
			if c.Name != "" {
				doc.complex[ns+" "+c.Name] = c
				doc.complex[c.Name] = c
			}
		}
		for _, s := range schema.SimpleTypes {
			if s.Name != "" {
				doc.simple[ns+" "+s.Name] = s
				doc.simple[s.Name] = s
			}
		}
	}
	for _, m := range def.Messages {
		doc.messages[m.Name] = m
	}
	versions := bindingVersions(data)
	type boundOp struct {
		action  string
		version string
	}
	byName := map[string][]boundOp{}
	seenBind := map[string]bool{}
	for _, b := range def.Bindings {
		ver := versions[b.Name]
		if ver == "" {
			ver = "1.1"
		}
		for _, op := range b.Operations {
			key := op.Name + "\x00" + ver
			if seenBind[key] {
				continue
			}
			seenBind[key] = true
			byName[op.Name] = append(byName[op.Name], boundOp{
				action:  strings.TrimSpace(op.Operation.SOAPAction),
				version: ver,
			})
		}
	}
	seen := map[string]bool{}
	for _, pt := range def.PortTypes {
		for _, op := range pt.Operations {
			if seen[op.Name] {
				continue
			}
			seen[op.Name] = true
			binds := byName[op.Name]
			if len(binds) == 0 {
				binds = []boundOp{{version: "1.1"}}
			}
			for _, b := range binds {
				built := wsdlOp{
					name:    op.Name,
					action:  b.action,
					version: b.version,
					tns:     def.TargetNamespace,
					outMsg:  op.Output.Message,
				}
				if built.version == "" {
					built.version = "1.1"
				}
				if msg, ok := doc.messages[localName(op.Input.Message)]; ok && len(msg.Parts) > 0 && msg.Parts[0].Element != "" {
					built.inNS, built.inLocal = doc.expand(msg.Parts[0].Element)
				} else {
					built.inLocal = op.Name
				}
				if msg, ok := doc.messages[localName(op.Output.Message)]; ok && len(msg.Parts) > 0 && msg.Parts[0].Element != "" {
					_, built.outLocal = doc.expand(msg.Parts[0].Element)
				} else if local := localName(op.Output.Message); local != "" {
					built.outLocal = local
				} else {
					built.outLocal = op.Name + "Response"
				}
				doc.ops = append(doc.ops, built)
			}
		}
	}
	return doc, nil
}

func (d *wsdlDoc) expand(q string) (ns, local string) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", ""
	}
	prefix, local, ok := strings.Cut(q, ":")
	if !ok {
		return d.prefixes[""], q
	}
	if ns, ok := d.prefixes[prefix]; ok {
		return ns, local
	}
	return "", local
}

func (d *wsdlDoc) responseInner(op wsdlOp, seed int64) string {
	local := op.outLocal
	ns := op.tns
	var inner string
	if msg, ok := d.messages[localName(op.outMsg)]; ok && len(msg.Parts) > 0 {
		part := msg.Parts[0]
		if part.Element != "" {
			ens, elocal := d.expand(part.Element)
			if el, ok := d.lookupElem(ens, elocal); ok {
				local = elocal
				if ens != "" {
					ns = ens
				}
				inner = d.innerXML(el, seed, 0)
			} else {
				local = elocal
				if ens != "" {
					ns = ens
				}
			}
		} else if part.Type != "" {
			inner = xmlEscape(d.scalar(part.Type, seed))
			local = part.Name
			if local == "" {
				local = op.outLocal
			}
		}
	}
	return wrapNS(local, ns, inner)
}

func (d *wsdlDoc) lookupElem(ns, local string) (xsdElement, bool) {
	if el, ok := d.elem[ns+" "+local]; ok {
		return el, true
	}
	el, ok := d.elemName[local]
	return el, ok
}

func (d *wsdlDoc) innerXML(el xsdElement, seed int64, depth int) string {
	if depth > 8 {
		return ""
	}
	if el.Ref != "" {
		ns, local := d.expand(el.Ref)
		found, ok := d.lookupElem(ns, local)
		if !ok {
			return ""
		}
		el = found
	}
	if el.Complex != nil {
		return d.complexXML(*el.Complex, seed, depth)
	}
	if el.Simple != nil {
		return xmlEscape(d.simpleText(*el.Simple, el.Type, seed))
	}
	if el.Type != "" {
		ns, local := d.expand(el.Type)
		if c, ok := d.complex[ns+" "+local]; ok {
			return d.complexXML(c, seed, depth)
		}
		if c, ok := d.complex[local]; ok && ns == "" {
			return d.complexXML(c, seed, depth)
		}
		if s, ok := d.simple[ns+" "+local]; ok {
			return xmlEscape(d.simpleText(s, el.Type, seed))
		}
		if s, ok := d.simple[local]; ok {
			return xmlEscape(d.simpleText(s, el.Type, seed))
		}
		return xmlEscape(d.scalar(el.Type, seed))
	}
	return xmlEscape(d.scalar("string", seed))
}

func (d *wsdlDoc) complexXML(c xsdComplex, seed int64, depth int) string {
	var b strings.Builder
	if c.ComplexContent.Extension != nil {
		ns, local := d.expand(c.ComplexContent.Extension.Base)
		if base, ok := d.complex[ns+" "+local]; ok {
			b.WriteString(d.complexXML(base, seed, depth+1))
		} else if base, ok := d.complex[local]; ok {
			b.WriteString(d.complexXML(base, seed, depth+1))
		}
		if c.ComplexContent.Extension.Sequence != nil {
			b.WriteString(d.groupXML(c.ComplexContent.Extension.Sequence.Elements, false, seed, depth))
		}
		return b.String()
	}
	switch {
	case c.Sequence != nil:
		b.WriteString(d.groupXML(c.Sequence.Elements, false, seed, depth))
	case c.All != nil:
		b.WriteString(d.groupXML(c.All.Elements, false, seed, depth))
	case c.Choice != nil:
		b.WriteString(d.groupXML(c.Choice.Elements, true, seed, depth))
	}
	return b.String()
}

func (d *wsdlDoc) groupXML(els []xsdElement, choice bool, seed int64, depth int) string {
	if choice && len(els) > 1 {
		els = els[:1]
	}
	var b strings.Builder
	for _, el := range els {
		b.WriteString(d.elementXML(el, seed, depth+1))
	}
	return b.String()
}

const maxSampleOccurs = 32

func (d *wsdlDoc) elementXML(el xsdElement, seed int64, depth int) string {
	name := el.Name
	minOccurs := el.MinOccurs
	if el.Ref != "" {
		ns, local := d.expand(el.Ref)
		found, ok := d.lookupElem(ns, local)
		if !ok {
			return ""
		}
		name = found.Name
		if name == "" {
			name = local
		}
		if minOccurs == "" {
			minOccurs = found.MinOccurs
		}
		el = found
		el.MinOccurs = minOccurs
	}
	if name == "" {
		return ""
	}
	// Past the recursion limit, omit optional elements and emit required
	// ones empty so a cyclic schema cannot grow without bound.
	if depth > 8 {
		if atoiDefault(el.MinOccurs, 1) == 0 {
			return ""
		}
		xn := xmlName(name)
		return "<" + xn + "></" + xn + ">"
	}
	repeats := 1
	if el.MaxOccurs == "unbounded" || atoiDefault(el.MaxOccurs, 1) > 1 {
		repeats = 1
		if seed%2 != 0 {
			repeats = 2
		}
		if n := atoiDefault(el.MaxOccurs, repeats); el.MaxOccurs != "unbounded" && n < repeats {
			repeats = n
		}
	}
	if min := atoiDefault(el.MinOccurs, 1); min > repeats {
		repeats = min
	}
	if repeats > maxSampleOccurs {
		repeats = maxSampleOccurs
	}
	if repeats < 0 {
		repeats = 0
	}
	var b strings.Builder
	inner := d.innerXML(el, seed, depth)
	xn := xmlName(name)
	for i := 0; i < repeats; i++ {
		b.WriteString("<" + xn + ">" + inner + "</" + xn + ">")
	}
	return b.String()
}

func (d *wsdlDoc) simpleText(s xsdSimple, fallback string, seed int64) string {
	if s.Restriction != nil && len(s.Restriction.Enums) > 0 {
		i := 0
		if seed > 0 {
			i = int(seed) % len(s.Restriction.Enums)
		}
		return s.Restriction.Enums[i].Value
	}
	base := fallback
	if s.Restriction != nil && s.Restriction.Base != "" {
		base = s.Restriction.Base
	}
	return d.scalar(base, seed)
}

func (d *wsdlDoc) scalar(typeName string, seed int64) string {
	local := localName(typeName)
	if local == "" {
		local = "string"
	}
	if seed < 0 {
		seed = 0
	}
	switch local {
	case "int", "integer", "long", "short", "byte", "unsignedInt", "unsignedLong",
		"unsignedShort", "unsignedByte", "nonNegativeInteger", "nonPositiveInteger",
		"negativeInteger", "float", "double", "decimal":
		return strconv.FormatInt(seed, 10)
	case "positiveInteger":
		if seed < 1 {
			return "1"
		}
		return strconv.FormatInt(seed, 10)
	case "boolean":
		if seed%2 == 1 {
			return "true"
		}
		return "false"
	case "date":
		return "2020-01-01"
	case "dateTime":
		return "2020-01-01T00:00:00Z"
	case "time":
		return "00:00:00Z"
	default:
		return "sample"
	}
}

func wrapNS(local, ns, inner string) string {
	name := xmlName(local)
	if ns == "" {
		return "<" + name + ">" + inner + "</" + name + ">"
	}
	return "<" + name + ` xmlns="` + xmlEscape(ns) + `">` + inner + "</" + name + ">"
}

func xmlName(s string) string {
	if s == "" {
		return "value"
	}
	for _, r := range s {
		switch r {
		case '<', '>', '&', '"', '\'', ' ', '\n', '\t':
			return "value"
		}
	}
	return s
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func localName(q string) string {
	q = strings.TrimSpace(q)
	if i := strings.LastIndex(q, ":"); i >= 0 {
		return q[i+1:]
	}
	return q
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

var xmlnsRe = regexp.MustCompile(`xmlns(?::([A-Za-z_][\w.-]*))?\s*=\s*("([^"]*)"|'([^']*)')`)

func xmlnsMap(data []byte) map[string]string {
	m := map[string]string{}
	for _, sm := range xmlnsRe.FindAllSubmatch(data, -1) {
		prefix := string(sm[1])
		val := string(sm[3])
		if val == "" {
			val = string(sm[4])
		}
		if _, ok := m[prefix]; !ok {
			m[prefix] = val
		}
	}
	return m
}

func bindingVersions(data []byte) map[string]string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	out := map[string]string{}
	var cur string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "binding" && cur != "" && ee.Name.Space != soap11NS && ee.Name.Space != soap12NS {
				cur = ""
			}
			continue
		}
		if se.Name.Local == "binding" && attr(se, "name") != "" && attr(se, "type") != "" {
			cur = attr(se, "name")
			continue
		}
		if cur == "" || se.Name.Local != "binding" {
			continue
		}
		switch se.Name.Space {
		case soap12NS:
			out[cur] = "1.2"
		case soap11NS:
			if out[cur] == "" {
				out[cur] = "1.1"
			}
		}
	}
	return out
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name && a.Name.Space == "" {
			return a.Value
		}
	}
	return ""
}
