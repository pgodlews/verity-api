package importers

// Independent clean-room importer. Not affiliated with or endorsed by
// SmartBear. Reads SoapUI OSS (EUPL) project files; contains no SoapUI code.

import (
	"encoding/xml"
	"strings"
)

// soapuiNS is the configuration namespace used by SoapUI project files.
// The element and attribute names below are the publicly visible format,
// not an expression of anyone's source.
const soapuiNS = "http://eviware.com/soapui/config"

// suProject is the subset of a SoapUI project file this importer reads.
// encoding/xml drops elements that are not listed here, so newer or unused
// parts of a file (settings, attachments, JMS defaults) do not fail the import.
type suProject struct {
	XMLName        xml.Name      `xml:"soapui-project"`
	Name           string        `xml:"name,attr"`
	Interfaces     []suInterface `xml:"http://eviware.com/soapui/config interface"`
	TestSuites     []suTestSuite `xml:"http://eviware.com/soapui/config testSuite"`
	Properties     suProperties  `xml:"http://eviware.com/soapui/config properties"`
	MockServices   []suNamed     `xml:"http://eviware.com/soapui/config mockService"`
	RESTMocks      []suNamed     `xml:"http://eviware.com/soapui/config restMockService"`
	OAuth2         suBlob        `xml:"http://eviware.com/soapui/config oAuth2ProfileContainer"`
	WSS            suBlob        `xml:"http://eviware.com/soapui/config wssContainer"`
	Settings       suBlob        `xml:"http://eviware.com/soapui/config settings"`
	SetupScript    string        `xml:"http://eviware.com/soapui/config setupScript"`
	TearDownScript string        `xml:"http://eviware.com/soapui/config tearDownScript"`
}

type suBlob struct {
	Inner string `xml:",innerxml"`
}

type suNamed struct {
	Name string `xml:"name,attr"`
}

type suInterface struct {
	Name        string        `xml:"name,attr"`
	Type        string        `xml:"type,attr"`
	SoapVersion string        `xml:"soapVersion,attr"`
	BasePath    string        `xml:"basePath,attr"`
	Endpoints   suEndpoints   `xml:"http://eviware.com/soapui/config endpoints"`
	Resources   []suResource  `xml:"http://eviware.com/soapui/config resource"`
	Operations  []suOperation `xml:"http://eviware.com/soapui/config operation"`
	Parameters  suParameters  `xml:"http://eviware.com/soapui/config parameters"`
}

type suEndpoints struct {
	Endpoint []string `xml:"http://eviware.com/soapui/config endpoint"`
}

type suResource struct {
	Name       string       `xml:"name,attr"`
	Path       string       `xml:"path,attr"`
	Methods    []suMethod   `xml:"http://eviware.com/soapui/config method"`
	Parameters suParameters `xml:"http://eviware.com/soapui/config parameters"`
	// Resources are child resources; they extend this path and inherit its
	// parameters.
	Resources []suResource `xml:"http://eviware.com/soapui/config resource"`
}

type suMethod struct {
	Name       string       `xml:"name,attr"`
	Method     string       `xml:"method,attr"`
	Parameters suParameters `xml:"http://eviware.com/soapui/config parameters"`
}

type suOperation struct {
	Name   string `xml:"name,attr"`
	Action string `xml:"action,attr"`
}

type suTestSuite struct {
	Name           string       `xml:"name,attr"`
	Disabled       string       `xml:"disabled,attr"`
	TestCases      []suTestCase `xml:"http://eviware.com/soapui/config testCase"`
	Properties     suProperties `xml:"http://eviware.com/soapui/config properties"`
	LoadTests      []suNamed    `xml:"http://eviware.com/soapui/config loadTest"`
	SecurityTests  []suNamed    `xml:"http://eviware.com/soapui/config securityTest"`
	SetupScript    string       `xml:"http://eviware.com/soapui/config setupScript"`
	TearDownScript string       `xml:"http://eviware.com/soapui/config tearDownScript"`
}

type suTestCase struct {
	Name           string       `xml:"name,attr"`
	Disabled       string       `xml:"disabled,attr"`
	TestSteps      []suTestStep `xml:"http://eviware.com/soapui/config testStep"`
	Properties     suProperties `xml:"http://eviware.com/soapui/config properties"`
	LoadTests      []suNamed    `xml:"http://eviware.com/soapui/config loadTest"`
	SecurityTests  []suNamed    `xml:"http://eviware.com/soapui/config securityTest"`
	SetupScript    string       `xml:"http://eviware.com/soapui/config setupScript"`
	TearDownScript string       `xml:"http://eviware.com/soapui/config tearDownScript"`
}

type suTestStep struct {
	Type     string   `xml:"type,attr"`
	Name     string   `xml:"name,attr"`
	Disabled string   `xml:"disabled,attr"`
	Config   suConfig `xml:"http://eviware.com/soapui/config config"`
}

// suConfig holds the children of every step kind we understand. Unused
// children stay zero; unknown children are ignored.
type suConfig struct {
	Service      string       `xml:"service,attr"`
	ResourcePath string       `xml:"resourcePath,attr"`
	MethodName   string       `xml:"methodName,attr"`
	Interface    string       `xml:"http://eviware.com/soapui/config interface"`
	Operation    string       `xml:"http://eviware.com/soapui/config operation"`
	Request      *suCall      `xml:"http://eviware.com/soapui/config request"`
	RestRequest  *suCall      `xml:"http://eviware.com/soapui/config restRequest"`
	Transfers    []suTransfer `xml:"http://eviware.com/soapui/config transfers"`
	Properties   suProperties `xml:"http://eviware.com/soapui/config properties"`
}

// suCall is a SOAP request or a REST request. Both carry an endpoint, an
// optional body, assertions, parameters, headers, and credentials.
type suCall struct {
	Name        string         `xml:"name,attr"`
	MediaType   string         `xml:"mediaType,attr"`
	Method      string         `xml:"method,attr"`
	SOAPAction  string         `xml:"soapAction,attr"`
	Endpoint    string         `xml:"http://eviware.com/soapui/config endpoint"`
	Body        string         `xml:"http://eviware.com/soapui/config request"`
	Assertions  []suAssertion  `xml:"http://eviware.com/soapui/config assertion"`
	Parameters  suParameters   `xml:"http://eviware.com/soapui/config parameters"`
	Settings    suSettings     `xml:"http://eviware.com/soapui/config settings"`
	Credentials suCredentials  `xml:"http://eviware.com/soapui/config credentials"`
	WSA         suWSA          `xml:"http://eviware.com/soapui/config wsaConfig"`
	Attachments []suAttachment `xml:"http://eviware.com/soapui/config attachment"`
}

// suAttachment counts a request attachment without keeping its bytes.
type suAttachment struct{}

func (suAttachment) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return d.Skip()
}

type suWSA struct {
	Action string `xml:"action,attr"`
}

type suCredentials struct {
	AuthType string `xml:"http://eviware.com/soapui/config authType"`
	Username string `xml:"http://eviware.com/soapui/config username"`
	Password string `xml:"http://eviware.com/soapui/config password"`
}

type suSettings struct {
	Items []suSetting `xml:"http://eviware.com/soapui/config setting"`
}

type suSetting struct {
	ID   string `xml:"id,attr"`
	Text string `xml:",chardata"`
}

type suAssertion struct {
	Type     string         `xml:"type,attr"`
	Name     string         `xml:"name,attr"`
	Disabled string         `xml:"disabled,attr"`
	Config   suAssertConfig `xml:"http://eviware.com/soapui/config configuration"`
}

type suTransfer struct {
	Name       string `xml:"http://eviware.com/soapui/config name"`
	SourceType string `xml:"http://eviware.com/soapui/config sourceType"`
	SourceStep string `xml:"http://eviware.com/soapui/config sourceStep"`
	SourcePath string `xml:"http://eviware.com/soapui/config sourcePath"`
	TargetType string `xml:"http://eviware.com/soapui/config targetType"`
	TargetStep string `xml:"http://eviware.com/soapui/config targetStep"`
	TargetPath string `xml:"http://eviware.com/soapui/config targetPath"`
}

type suProperty struct {
	Name  string
	Value string
}

type suProperties struct {
	Property []suProperty
}

type suParam struct {
	Name    string
	Value   string
	Style   string
	Default string
}

type suParameters struct {
	Params []suParam
}

// suAssertConfig is the inside of an assertion. Those children are usually
// in no namespace, so matching is by local name.
type suAssertConfig struct {
	Codes          string
	SLA            string
	Path           string
	Content        string
	Token          string
	IgnoreCase     string
	UseRegEx       string
	AllowWildcards string
	IgnoreNS       string
	IgnoreComments string
	Definition     string
}

func (p *suProperties) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == start.Name.Local {
				return nil
			}
			continue
		}
		if se.Name.Local != "property" {
			if err := d.Skip(); err != nil {
				return err
			}
			continue
		}
		var prop suProperty
		if err := d.DecodeElement(&prop, &se); err != nil {
			return err
		}
		p.Property = append(p.Property, prop)
	}
}

func (p *suProperty) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == start.Name.Local {
				return nil
			}
			continue
		}
		switch se.Name.Local {
		case "name", "value":
			text, err := readXMLScalar(d, se)
			if err != nil {
				return err
			}
			if se.Name.Local == "name" {
				p.Name = text
			} else {
				p.Value = text
			}
		default:
			if err := d.Skip(); err != nil {
				return err
			}
		}
	}
}

func (p *suParameters) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == start.Name.Local {
				return nil
			}
			continue
		}
		switch se.Name.Local {
		case "entry":
			param := suParam{}
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "key":
					param.Name = a.Value
				case "value":
					param.Value = a.Value
				}
			}
			p.Params = append(p.Params, param)
			if err := d.Skip(); err != nil {
				return err
			}
		case "parameter":
			param, err := decodeParam(d, se)
			if err != nil {
				return err
			}
			p.Params = append(p.Params, param)
		default:
			if err := d.Skip(); err != nil {
				return err
			}
		}
	}
}

func decodeParam(d *xml.Decoder, start xml.StartElement) (suParam, error) {
	var p suParam
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "name":
			p.Name = a.Value
		case "value":
			p.Value = a.Value
		case "style":
			p.Style = a.Value
		}
	}
	for {
		tok, err := d.Token()
		if err != nil {
			return p, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == start.Name.Local {
				p.Name = strings.TrimSpace(p.Name)
				p.Style = strings.TrimSpace(p.Style)
				p.Value = strings.TrimSpace(p.Value)
				return p, nil
			}
			continue
		}
		switch se.Name.Local {
		case "name", "value", "style", "default":
			text, err := readXMLScalar(d, se)
			if err != nil {
				return p, err
			}
			switch se.Name.Local {
			case "name":
				p.Name = text
			case "value":
				p.Value = text
			case "style":
				p.Style = text
			case "default":
				p.Default = text
			}
		default:
			if err := d.Skip(); err != nil {
				return p, err
			}
		}
	}
}

func (c *suAssertConfig) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == start.Name.Local {
				return nil
			}
			continue
		}
		switch se.Name.Local {
		case "codes", "SLA", "path", "content", "token", "ignoreCase", "useRegEx",
			"allowWildcards", "ignoreNamspaceDifferences", "ignoreNamespaceDifferences",
			"ignoreComments", "definition":
			text, err := readXMLScalar(d, se)
			if err != nil {
				return err
			}
			switch se.Name.Local {
			case "codes":
				c.Codes = text
			case "SLA":
				c.SLA = text
			case "path":
				c.Path = text
			case "content":
				c.Content = text
			case "token":
				c.Token = text
			case "ignoreCase":
				c.IgnoreCase = text
			case "useRegEx":
				c.UseRegEx = text
			case "allowWildcards":
				c.AllowWildcards = text
			case "ignoreNamspaceDifferences", "ignoreNamespaceDifferences":
				c.IgnoreNS = text
			case "ignoreComments":
				c.IgnoreComments = text
			case "definition":
				c.Definition = text
			}
		default:
			if err := d.Skip(); err != nil {
				return err
			}
		}
	}
}

// readXMLScalar reads an element's character data with entities and CDATA
// decoded. Nested elements are re-serialized so their text is decoded too;
// a scalar is not left as raw markup such as "&amp;" or "<![CDATA[...]]>".
func readXMLScalar(d *xml.Decoder, start xml.StartElement) (string, error) {
	var b strings.Builder
	if err := writeScalarChildren(d, start.Name, &b, true); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

func writeScalarChildren(d *xml.Decoder, end xml.Name, b *strings.Builder, top bool) error {
	return writeScalarContent(d, end, b, top, "")
}

// writeScalarContent copies nested markup. The decoder has already resolved
// prefixes to namespace URIs, so each element re-declares its namespace with
// xmlns="URI" where it differs from its parent's: the prefix is lost but the
// namespace (what XPath matching depends on) is preserved.
func writeScalarContent(d *xml.Decoder, end xml.Name, b *strings.Builder, top bool, ns string) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.CharData:
			if top {
				b.Write(t)
			} else if err := xml.EscapeText(b, t); err != nil {
				return err
			}
		case xml.StartElement:
			if err := writeScalarElement(d, t, b, ns); err != nil {
				return err
			}
		case xml.EndElement:
			if t.Name.Local == end.Local {
				return nil
			}
		}
	}
}

func writeScalarElement(d *xml.Decoder, start xml.StartElement, b *strings.Builder, parentNS string) error {
	b.WriteByte('<')
	b.WriteString(start.Name.Local)
	if start.Name.Space != parentNS {
		b.WriteString(` xmlns="`)
		if err := xml.EscapeText(b, []byte(start.Name.Space)); err != nil {
			return err
		}
		b.WriteByte('"')
	}
	for _, a := range start.Attr {
		// Namespace declarations are re-expressed above via xmlns="URI".
		if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(a.Name.Local)
		b.WriteString(`="`)
		if err := xml.EscapeText(b, []byte(a.Value)); err != nil {
			return err
		}
		b.WriteByte('"')
	}
	b.WriteByte('>')
	if err := writeScalarContent(d, start.Name, b, false, start.Name.Space); err != nil {
		return err
	}
	b.WriteString("</")
	b.WriteString(start.Name.Local)
	b.WriteByte('>')
	return nil
}
