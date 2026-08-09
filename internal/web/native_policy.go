package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"m365-native/internal/chathub"
)

const nativePolicySchemaV1 = "m365-native-policy/v1"

var errReservedNativeToolName = errors.New("caller tool name is reserved for a native capability")

func validateReservedNativeToolName(name string) error {
	if strings.EqualFold(strings.TrimSpace(name), "BingWebSearch") {
		return fmt.Errorf("%w: BingWebSearch", errReservedNativeToolName)
	}
	return nil
}

type CapabilityPolicy string

type EnforcementLevel string

const (
	PolicyInherit  CapabilityPolicy = "inherit"
	PolicyEnabled  CapabilityPolicy = "enabled"
	PolicyDisabled CapabilityPolicy = "disabled"

	EnforcementNone      EnforcementLevel = "none"
	EnforcementPrompt    EnforcementLevel = "prompt_only"
	EnforcementExecution EnforcementLevel = "execution_enforced"
)

type NativeCapabilityPolicy struct {
	ClientExecutionTools CapabilityPolicy `json:"client_execution_tools"`
	NativeSearch         CapabilityPolicy `json:"native_search"`
	NativeGrounding      CapabilityPolicy `json:"native_grounding"`
	NativeReadTools      CapabilityPolicy `json:"native_read_tools"`
	NativeMutationTools  CapabilityPolicy `json:"native_mutation_tools"`
}

type nativePolicySnapshot struct {
	Schema                       string                 `json:"schema"`
	RequestedToolPolicy          string                 `json:"requested_tool_policy"`
	EffectiveToolPolicy          string                 `json:"effective_tool_policy"`
	NativeSearchStatus           string                 `json:"native_search_status"`
	NativeMutationStatus         string                 `json:"native_mutation_status"`
	RequestedCapabilities        NativeCapabilityPolicy `json:"requested_capabilities"`
	EffectiveCapabilities        NativeCapabilityPolicy `json:"effective_capabilities"`
	MutationEnforcement          EnforcementLevel       `json:"mutation_enforcement"`
	MutationEnforcementVerified  bool                   `json:"mutation_enforcement_verified"`
	MutationEnforcementScope     string                 `json:"mutation_enforcement_scope,omitempty"`
	UpstreamNativeMutationStatus string                 `json:"upstream_native_mutation_status,omitempty"`
}

func resolveNativePolicy(planningMode string, tools []chathub.Tool) (nativePolicySnapshot, error) {
	planningMode = strings.ToLower(strings.TrimSpace(planningMode))
	if planningMode != "router" && planningMode != "native" {
		return nativePolicySnapshot{}, fmt.Errorf("unsupported tool planning mode %q", planningMode)
	}
	for _, tool := range tools {
		if err := validateReservedNativeToolName(nativePolicyToolName(tool)); err != nil {
			return nativePolicySnapshot{}, err
		}
	}

	requested := "none"
	effective := planningMode
	mutationStatus := "not_requested"
	if len(tools) > 0 {
		requested = "declared_tools"
		if customExecOnly(tools) {
			requested = "custom_exec_only"
			effective = "custom_exec_only"
		} else if declaredNativeMutation(tools) {
			mutationStatus = "unverified"
		}
	}

	return nativePolicySnapshot{
		Schema:               nativePolicySchemaV1,
		RequestedToolPolicy:  requested,
		EffectiveToolPolicy:  effective,
		NativeSearchStatus:   "unverified",
		NativeMutationStatus: mutationStatus,
		RequestedCapabilities: NativeCapabilityPolicy{
			ClientExecutionTools: PolicyEnabled,
			NativeSearch:         PolicyInherit,
			NativeGrounding:      PolicyInherit,
			NativeReadTools:      PolicyInherit,
			NativeMutationTools:  PolicyDisabled,
		},
		EffectiveCapabilities: NativeCapabilityPolicy{
			ClientExecutionTools: PolicyEnabled,
			NativeSearch:         PolicyInherit,
			NativeGrounding:      PolicyInherit,
			NativeReadTools:      PolicyInherit,
			NativeMutationTools:  PolicyInherit,
		},
		MutationEnforcement:         EnforcementNone,
		MutationEnforcementVerified: false,
	}, nil
}

func withSidecarExecutionEnforcement(policy nativePolicySnapshot) nativePolicySnapshot {
	policy.EffectiveCapabilities.NativeMutationTools = PolicyDisabled
	policy.MutationEnforcement = EnforcementExecution
	policy.MutationEnforcementVerified = true
	policy.MutationEnforcementScope = "sidecar_tool_emission"
	policy.UpstreamNativeMutationStatus = "unverified"
	return policy
}

func resolveResponsesNativePolicy(planningMode string, tools []chathub.Tool) (nativePolicySnapshot, error) {
	policy, err := resolveNativePolicy(planningMode, tools)
	if err != nil {
		return nativePolicySnapshot{}, err
	}
	if customExecOnly(tools) {
		policy.MutationEnforcement = EnforcementPrompt
	}
	return policy, nil
}

func customExecOnly(tools []chathub.Tool) bool {
	if len(tools) == 0 {
		return false
	}
	for _, tool := range tools {
		name := nativePolicyToolName(tool)
		if tool.Type != "custom" || name != "exec" {
			return false
		}
	}
	return true
}

func declaredNativeMutation(tools []chathub.Tool) bool {
	for _, tool := range tools {
		if tool.Type == "custom" {
			continue
		}
		name := nativePolicyToolName(tool)
		if name == "" || toolLooksMutating(name) || !toolLooksReadOnly(name) {
			return true
		}
	}
	return false
}

func nativePolicyToolName(tool chathub.Tool) string {
	var definition struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(tool.Function, &definition) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(definition.Name))
}

func withNativePolicy(metadata map[string]any, policy nativePolicySnapshot) map[string]any {
	metadata["native_policy"] = policy
	return metadata
}
