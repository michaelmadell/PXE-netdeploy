package unattend

import (
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestRenderName(t *testing.T) {
	d := NameData{Client: NameClient{Serial: "ABC123456789", MAC: "00:15:5d:00:85:09"}, Variables: map[string]string{"site": "ldn"}}
	for _, c := range []struct {
		pattern, want string
		bad           bool
	}{
		{`LAB-{{ .Client.Serial | last 6 }}`, "LAB-456789", false},
		{`{{ .Variables.site | upper }}-{{ .Client.Serial | first 4 | lower }}`, "LDN-abc1", false},
		{`{{ .Variables.missing }}`, "", true},           // missing variable
		{`{{ .Client.Serial }}-TOO-LONG-NAME`, "", true}, // over 15
		{`LAB-`, "", true},  // trailing hyphen
		{`12345`, "", true}, // all digits
		{`LAB_{{ .Client.Serial | last 2 }}`, "", true},   // underscore
		{`{{ .Client.Serial `, "", true},                  // doesn't parse
		{`LAB-{{ .Client.Hostname | last 4 }}`, "", true}, // the client has no hostname
	} {
		got, err := RenderName(c.pattern, d)
		if c.bad != (err != nil) || (!c.bad && got != c.want) {
			t.Errorf("%s: %q %v", c.pattern, got, err)
		}
	}
	if _, err := RenderName(`12345`, d); !errors.Is(err, ErrInvalidName) {
		t.Errorf("all digits: %v", err)
	}
}

func parseXML(t *testing.T, b []byte) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(string(b)))
	for {
		if _, err := d.Token(); err == io.EOF {
			return
		} else if err != nil {
			t.Fatalf("not well-formed: %v\n%s", err, b)
		}
	}
}

func encoded(p string) string {
	u := utf16.Encode([]rune(p + "Password"))
	b := make([]byte, 0, 2*len(u))
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestRenderLocal(t *testing.T) {
	p, err := Parse([]byte(`{"name":"Lab","identity":{"mode":"local","accounts":[{"name":"admin","group":"Administrators","passwordSecretId":"s1"}],
		"autoLogon":{"account":"admin"}},"osSettings":{"productKeySecretId":"k1","timeZone":"W. Europe Standard Time"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if refs := p.Secrets(nil); len(refs) != 2 || refs[0].Kind != KindProductKey || refs[1].ID != "s1" {
		t.Errorf("secrets %+v", refs)
	}
	name := "LAB-01"
	b, err := Render(p, Input{ComputerName: &name, Secrets: map[string][]byte{"s1": []byte("p<&>w"), "k1": []byte("AAAAA-BBBBB-CCCCC-DDDDD-EEEEE\n")}})
	if err != nil {
		t.Fatal(err)
	}
	parseXML(t, b)
	s := string(b)
	for _, want := range []string{
		`<ComputerName>LAB-01</ComputerName>`, `<TimeZone>W. Europe Standard Time</TimeZone>`,
		`<ProductKey>AAAAA-BBBBB-CCCCC-DDDDD-EEEEE</ProductKey>`, `<UILanguage>en-GB</UILanguage>`,
		`<HideOnlineAccountScreens>true</HideOnlineAccountScreens>`, `<Name>admin</Name>`,
		`<Value>` + encoded("p<&>w") + `</Value>`, `<LogonCount>1</LogonCount>`, `processorArchitecture="amd64"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s\n%s", want, s)
		}
	}
	if strings.Contains(s, "p<&>w") {
		t.Error("password in clear")
	}
	if _, err := Render(p, Input{Secrets: map[string][]byte{"k1": []byte("x")}}); err == nil {
		t.Error("missing secret: no error")
	}
}

func TestRenderADAndNone(t *testing.T) {
	p, _ := Parse([]byte(`{"identity":{"mode":"ad","joinMethod":"credentials","domain":"corp.example","organizationalUnit":"OU=Labs,DC=corp,DC=example",
		"joinCredentialSecretId":"c1","localAdmin":{"name":"breakglass","passwordSecretId":"s2"}}}`))
	if refs := p.Secrets(nil); len(refs) != 2 {
		t.Errorf("secrets %+v", refs)
	}
	b, err := Render(p, Input{Arch: "arm64", Secrets: map[string][]byte{
		"c1": []byte(`{"username":"CORP\\joiner","password":"j&p"}`), "s2": []byte("bg")}})
	if err != nil {
		t.Fatal(err)
	}
	parseXML(t, b)
	s := string(b)
	for _, want := range []string{`<ComputerName>*</ComputerName>`, `<Domain>CORP</Domain>`, `<Username>joiner</Username>`,
		`<Password>j&amp;p</Password>`, `<JoinDomain>corp.example</JoinDomain>`, `<MachineObjectOU>OU=Labs,DC=corp,DC=example</MachineObjectOU>`,
		`<Name>breakglass</Name>`, `processorArchitecture="arm64"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s\n%s", want, s)
		}
	}

	odj := "blob1"
	p, _ = Parse([]byte(`{"identity":{"mode":"ad","joinMethod":"odj"}}`))
	if refs := p.Secrets(&odj); len(refs) != 1 || refs[0].Kind != KindODJBlob {
		t.Errorf("odj secrets %+v", refs)
	}
	if b, err = Render(p, Input{}); err != nil || strings.Contains(string(b), "UnattendedJoin") {
		t.Errorf("odj: %v\n%s", err, b)
	}

	p, _ = Parse([]byte(`{"identity":{"mode":"none","skipEula":false}}`))
	b, err = Render(p, Input{})
	if err != nil || !strings.Contains(string(b), `<HideEULAPage>false</HideEULAPage>`) || strings.Contains(string(b), "UserAccounts") {
		t.Errorf("none: %v\n%s", err, b)
	}
	if d, u := SplitAccount("joiner@corp.example", "x"); d != "corp.example" || u != "joiner" {
		t.Errorf("upn: %s %s", d, u)
	}
	p, _ = Parse([]byte(`{"identity":{"mode":"entra","method":"autopilot-registered"}}`))
	if _, err := Render(p, Input{}); err == nil {
		t.Error("entra: no error")
	}
}
