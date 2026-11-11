package idl

import "testing"

func TestProtoGoNameIsTheNameProtocGenGoEmits(t *testing.T) {
	for in, want := range map[string]string{
		"Req":      "Req",
		"user_req": "UserReq",
		"HTTPReq":  "HTTPReq",
		"_req":     "XReq",
		"v2_req":   "V2Req",
		"a_1b":     "A_1B",
	} {
		if got := ProtoGoName(in); got != want {
			t.Errorf("ProtoGoName(%q) = %q, want %q", in, got, want)
		}
	}
}
