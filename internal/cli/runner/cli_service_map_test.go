package runner

import (
	"strings"
	"testing"

	"github.com/tradalab/scorix/idl"
)

func TestAServiceCLIRefusesAMapFieldAsItsArgumentOrSecret(t *testing.T) {
	for name, opt := range map[string]string{"arg": `arg: "tags"`, "secret": `secret: "tags"`} {
		src := `syntax = "proto3";
package acme;
import "scorix/v1/annotations.proto";
message Empty {}
message Req { string id = 1; map<string, string> tags = 2; }
service S {
  // Doc.
  rpc A (Req) returns (Empty) { option (scorix.v1.cli) = { ` + opt + ` }; }
}
`
		pf, err := idl.Parse("app.proto", src)
		if err != nil {
			t.Fatal(err)
		}
		svc := pf.Services[0]
		if err := checkCLI(pf, svc.Name, svc.RPCs[0]); err == nil || !strings.Contains(err.Error(), "tags") {
			t.Errorf("%s: a map field was accepted: %v", name, err)
		}
	}
}
