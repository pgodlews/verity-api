package mock

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const calcWSDL = `<?xml version="1.0" encoding="UTF-8"?>
<definitions targetNamespace="urn:example:calc"
    xmlns="http://schemas.xmlsoap.org/wsdl/"
    xmlns:tns="urn:example:calc"
    xmlns:xsd="http://www.w3.org/2001/XMLSchema"
    xmlns:soap="http://schemas.xmlsoap.org/wsdl/soap/">
  <types>
    <xsd:schema targetNamespace="urn:example:calc" xmlns:xsd="http://www.w3.org/2001/XMLSchema">
      <xsd:element name="Add">
        <xsd:complexType>
          <xsd:sequence>
            <xsd:element name="a" type="xsd:int"/>
            <xsd:element name="b" type="xsd:int"/>
          </xsd:sequence>
        </xsd:complexType>
      </xsd:element>
      <xsd:element name="AddResponse">
        <xsd:complexType>
          <xsd:sequence>
            <xsd:element name="result" type="xsd:int"/>
          </xsd:sequence>
        </xsd:complexType>
      </xsd:element>
      <xsd:element name="Echo">
        <xsd:complexType><xsd:sequence>
          <xsd:element name="msg" type="xsd:string"/>
        </xsd:sequence></xsd:complexType>
      </xsd:element>
      <xsd:element name="EchoResponse">
        <xsd:complexType><xsd:sequence>
          <xsd:element name="msg" type="xsd:string"/>
        </xsd:sequence></xsd:complexType>
      </xsd:element>
    </xsd:schema>
  </types>
  <message name="AddRequest"><part name="parameters" element="tns:Add"/></message>
  <message name="AddResponse"><part name="parameters" element="tns:AddResponse"/></message>
  <message name="EchoRequest"><part name="parameters" element="tns:Echo"/></message>
  <message name="EchoResponse"><part name="parameters" element="tns:EchoResponse"/></message>
  <portType name="Calc">
    <operation name="Add"><input message="tns:AddRequest"/><output message="tns:AddResponse"/></operation>
    <operation name="Echo"><input message="tns:EchoRequest"/><output message="tns:EchoResponse"/></operation>
  </portType>
  <binding name="CalcBinding" type="tns:Calc">
    <soap:binding style="document" transport="http://schemas.xmlsoap.org/soap/http"/>
    <operation name="Add"><soap:operation soapAction="urn:example:calc:Add"/></operation>
    <operation name="Echo"><soap:operation soapAction="urn:example:calc:Echo"/></operation>
  </binding>
  <service name="Calc">
    <port name="CalcPort" binding="tns:CalcBinding">
      <soap:address location="http://example.com/calc"/>
    </port>
  </service>
</definitions>`

const calc12 = `<?xml version="1.0" encoding="UTF-8"?>
<definitions targetNamespace="urn:example:calc"
    xmlns="http://schemas.xmlsoap.org/wsdl/"
    xmlns:tns="urn:example:calc"
    xmlns:soap12="http://schemas.xmlsoap.org/wsdl/soap12/">
  <message name="PingRequest"/>
  <message name="PingResponse"/>
  <portType name="P">
    <operation name="Ping"><input message="tns:PingRequest"/><output message="tns:PingResponse"/></operation>
  </portType>
  <binding name="B" type="tns:P">
    <soap12:binding style="document" transport="http://schemas.xmlsoap.org/soap/http"/>
    <operation name="Ping"><soap12:operation soapAction="urn:example:calc:Ping"/></operation>
  </binding>
  <service name="S"><port name="P" binding="tns:B">
    <soap12:address location="http://example.com/calc"/>
  </port></service>
</definitions>`

func TestSOAPDispatchAndFault(t *testing.T) {
	h := mustHandler(t, []byte(calcWSDL), Options{Seed: 0})
	code, ct, body := call(t, h, http.MethodPost, "/", map[string]string{
		"Content-Type": "text/xml; charset=utf-8",
		"SOAPAction":   `"urn:example:calc:Add"`,
	}, "")
	if code != 200 || !strings.Contains(ct, "text/xml") {
		t.Fatalf("add action: %d %s %s", code, ct, body)
	}
	if !strings.Contains(string(body), "AddResponse") || !strings.Contains(string(body), "<result>0</result>") {
		t.Fatalf("envelope: %s", body)
	}
	if !strings.Contains(string(body), "http://schemas.xmlsoap.org/soap/envelope/") {
		t.Fatalf("soap 1.1 namespace missing: %s", body)
	}

	envelope := `<?xml version="1.0"?><soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><Echo xmlns="urn:example:calc"><msg>hi</msg></Echo></soap:Body></soap:Envelope>`
	code, _, body = call(t, h, http.MethodPost, "/calc", map[string]string{"Content-Type": "text/xml"}, envelope)
	if code != 200 || !strings.Contains(string(body), "EchoResponse") || !strings.Contains(string(body), "<msg>sample</msg>") {
		t.Fatalf("qname dispatch: %d %s", code, body)
	}

	code, ct, body = call(t, h, http.MethodPost, "/", map[string]string{
		"Content-Type": "text/xml",
		"SOAPAction":   `"urn:example:calc:Missing"`,
	}, envelope)
	if code != 500 || !strings.Contains(string(body), "unknown operation") || !strings.Contains(string(body), "faultstring") {
		t.Fatalf("fault: %d %s %s", code, ct, body)
	}

	h12 := mustHandler(t, []byte(calc12), Options{})
	code, ct, body = call(t, h12, http.MethodPost, "/", map[string]string{
		"Content-Type": `application/soap+xml; charset=utf-8; action="urn:example:calc:Ping"`,
	}, "")
	if code != 200 || !strings.Contains(ct, "application/soap+xml") || !strings.Contains(string(body), `xmlns:soap="http://www.w3.org/2003/05/soap-envelope"`) {
		t.Fatalf("soap 1.2: %d %s %s", code, ct, body)
	}
	code, _, body = call(t, h12, http.MethodPost, "/", map[string]string{"Content-Type": "application/soap+xml"}, "")
	if code != 500 || !strings.Contains(string(body), "soap:Sender") || !strings.Contains(string(body), "unknown operation") {
		t.Fatalf("soap 1.2 fault: %d %s", code, body)
	}
}

func TestCalculatorFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "specs", "calculator.wsdl"))
	if err != nil {
		t.Fatal(err)
	}
	h := mustHandler(t, data, Options{})
	code, ct, body := call(t, h, http.MethodPost, "/", map[string]string{
		"SOAPAction":   `"http://example.com/calculator/Add"`,
		"Content-Type": "text/xml",
	}, "")
	if code != 200 || !strings.Contains(ct, "text/xml") || !strings.Contains(string(body), "AddResponse") {
		t.Fatalf("fixture: %d %s %s", code, ct, body)
	}
}
