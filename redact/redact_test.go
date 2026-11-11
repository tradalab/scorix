package redact

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/tradalab/scorix/internal/redactpb"
)

func account() *redactpb.Account {
	return &redactpb.Account{
		Name: "ops", ApiKey: "sk-live", Phone: 84901234567, Aliases: []string{"a", "b"},
		Owner:   &redactpb.Owner{Email: "ana@example.com", Password: "hunter2", Team: "core"},
		Members: []*redactpb.Owner{{Email: "bob@example.com", Password: "pw", Team: "edge"}},
	}
}

func TestLogClearsSecretsAndMasksPII(t *testing.T) {
	in := account()
	got := Log(in).(*redactpb.Account)
	want := &redactpb.Account{
		Name:    "ops",
		Owner:   &redactpb.Owner{Email: Masked, Team: "core"},
		Members: []*redactpb.Owner{{Email: Masked, Team: "edge"}},
	}
	if !proto.Equal(got, want) {
		t.Fatalf("Log =\n%v\nwant\n%v", got, want)
	}
	if !proto.Equal(in, account()) {
		t.Fatal("Log changed the message it was given")
	}
}

func TestAgentClearsSecretsButKeepsPII(t *testing.T) {
	got := Agent(account()).(*redactpb.Account)
	want := account()
	want.ApiKey, want.Owner.Password, want.Members[0].Password = "", "", ""
	if !proto.Equal(got, want) {
		t.Fatalf("Agent =\n%v\nwant\n%v", got, want)
	}
	if Agent(nil) != nil || Log(nil) != nil {
		t.Fatal("a nil message came back non-nil")
	}
}

func TestMapValuesAreWalked(t *testing.T) {
	in := &redactpb.Account{
		ByTeam: map[string]*redactpb.Owner{"core": {Email: "ana@example.com", Password: "hunter2", Team: "core"}},
		Labels: map[string]string{"home": "12 Le Loi"},
	}
	got := Log(in).(*redactpb.Account)
	want := &redactpb.Account{ByTeam: map[string]*redactpb.Owner{"core": {Email: Masked, Team: "core"}}}
	if !proto.Equal(got, want) {
		t.Fatalf("Log =\n%v\nwant\n%v", got, want)
	}
	agent := Agent(in).(*redactpb.Account)
	if agent.ByTeam["core"].Password != "" || agent.ByTeam["core"].Email != "ana@example.com" || agent.Labels["home"] != "12 Le Loi" {
		t.Fatalf("Agent = %v", agent)
	}
}

func TestAnAnyIsOpenedAndWalked(t *testing.T) {
	inner, err := anypb.New(&redactpb.Owner{Email: "ana@example.com", Password: "hunter2", Team: "core"})
	if err != nil {
		t.Fatal(err)
	}
	got := Log(&redactpb.Account{Extra: inner}).(*redactpb.Account)
	var o redactpb.Owner
	if err := got.Extra.UnmarshalTo(&o); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(&o, &redactpb.Owner{Email: Masked, Team: "core"}) {
		t.Fatalf("inside the Any: %v", &o)
	}
}

func TestAnAnyOfAnUnknownTypeIsCleared(t *testing.T) {
	in := &redactpb.Account{Name: "ops", Extra: &anypb.Any{TypeUrl: "type.googleapis.com/acme.Unknown", Value: []byte{0x0a, 0x03, 'k', 'e', 'y'}}}
	got := Log(in).(*redactpb.Account)
	if got.Extra != nil || got.Name != "ops" {
		t.Fatalf("Log = %v, want the Any gone and the rest kept", got)
	}
}
