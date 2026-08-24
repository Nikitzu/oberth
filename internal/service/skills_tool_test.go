package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/oberthci/oberth/internal/api"
)

func TestSkillToolsAcceptEveryParameterTheirSchemaAdvertises(t *testing.T) {
	t.Parallel()
	service := &API{}
	actor := api.Actor{Identity: "tester"}
	for name, arguments := range map[string]string{
		"skills":    `{}`,
		"skill_get": `{"name":"oberth-triage"}`,
	} {
		if _, err := service.CallTool(context.Background(), actor, name, json.RawMessage(arguments)); err != nil {
			t.Fatalf("%s rejected %s: %v", name, arguments, err)
		}
	}
}

func TestSkillToolsRefuseAnUndeclaredParameter(t *testing.T) {
	t.Parallel()
	service := &API{}
	actor := api.Actor{Identity: "tester"}
	for name, arguments := range map[string]string{
		"skills":    `{"nonsense":true}`,
		"skill_get": `{"name":"oberth-triage","nonsense":true}`,
	} {
		_, err := service.CallTool(context.Background(), actor, name, json.RawMessage(arguments))
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("%s accepted an undeclared parameter: %v", name, err)
		}
	}
}

func TestSkillGetRefusesAnUnknownName(t *testing.T) {
	t.Parallel()
	service := &API{}
	_, err := service.CallTool(context.Background(), api.Actor{Identity: "tester"},
		"skill_get", json.RawMessage(`{"name":"../../etc/passwd"}`))
	if err == nil {
		t.Fatal("skill_get accepted a traversal name")
	}
}

func TestSkillsNeedsNoServerState(t *testing.T) {
	t.Parallel()
	service := &API{}
	result, err := service.CallTool(context.Background(), api.Actor{Identity: "tester"},
		"skills", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("skills required state a bare API does not have: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "oberth-triage") {
		t.Fatalf("catalogue missing: %s", encoded)
	}
}
