package unattend

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/builtin from the current output")

// goldenCase is a profile and its deployment inputs whose built-in output
// is pinned in testdata/builtin: the built-in template must keep producing
// the Phase 4 unattend byte for byte (ADR 0027).
type goldenCase struct {
	name, profile string
	in            Input
}

func goldenCases() []goldenCase {
	name := "LAB-01"
	amp := "A&B-01"
	return []goldenCase{
		{"none", `{"identity":{"mode":"none"}}`, Input{}},
		{"none-eula", `{"identity":{"mode":"none","skipEula":false},"osSettings":{"uiLanguage":"de-DE","systemLocale":"de-DE","userLocale":"de-DE","inputLocale":"0407:00000407","timeZone":"W. Europe Standard Time"}}`,
			Input{ComputerName: &name}},
		{"local", `{"identity":{"mode":"local","accounts":[{"name":"admin","displayName":"Lab <Admin>","group":"Administrators","passwordSecretId":"s1"},{"name":"user","group":"Users","passwordSecretId":"s2"}],"autoLogon":{"account":"admin","count":3}},"osSettings":{"productKeySecretId":"k1"}}`,
			Input{ComputerName: &amp, Secrets: map[string][]byte{"s1": []byte("p<&>w"), "s2": []byte("ü"), "k1": []byte("AAAAA-BBBBB-CCCCC-DDDDD-EEEEE\n")}}},
		{"local-oobe", `{"identity":{"mode":"local","hideOobe":false,"accounts":[{"name":"admin","group":"Administrators","passwordSecretId":"s1"}]}}`,
			Input{Secrets: map[string][]byte{"s1": []byte("x")}}},
		{"ad-credentials", `{"identity":{"mode":"ad","joinMethod":"credentials","domain":"corp.example","organizationalUnit":"OU=Labs,DC=corp,DC=example","joinCredentialSecretId":"c1","localAdmin":{"name":"breakglass","passwordSecretId":"s2"}}}`,
			Input{Arch: "arm64", ComputerName: &name, Secrets: map[string][]byte{"c1": []byte(`{"username":"CORP\\joiner","password":"j&p"}`), "s2": []byte("bg")}}},
		{"ad-credentials-upn", `{"identity":{"mode":"ad","joinMethod":"credentials","domain":"corp.example","joinCredentialSecretId":"c1"}}`,
			Input{Secrets: map[string][]byte{"c1": []byte(`{"username":"joiner@corp.example","password":"pw"}`)}}},
		{"ad-odj", `{"identity":{"mode":"ad","joinMethod":"odj","localAdmin":{"name":"breakglass","passwordSecretId":"s2"}}}`,
			Input{ComputerName: &name, ODJBlob: odjFile(true), Secrets: map[string][]byte{"s2": []byte("bg")}}},
	}
}

func TestBuiltinGolden(t *testing.T) {
	for _, c := range goldenCases() {
		p, err := Parse([]byte(c.profile))
		if err != nil {
			t.Fatal(err)
		}
		got, err := Render(p, c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		path := filepath.Join("testdata", "builtin", c.name+".xml")
		if *update {
			os.MkdirAll(filepath.Dir(path), 0o755)
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s: output differs from %s\n--- got\n%s", c.name, path, got)
		}
	}
}
