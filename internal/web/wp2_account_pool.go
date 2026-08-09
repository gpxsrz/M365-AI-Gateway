package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"m365-native/internal/auth"
	"m365-native/internal/chathub"
	"m365-native/internal/evidence"
)

type WP2AccountPoolHarnessOptions struct {
	Binding   evidence.CaptureBinding
	Routes    []string
	Protocols []string
}

type wp2AccountPoolProfileScenario struct {
	accountProfileRef string
	status            evidence.AccountPoolProfileStatus
	unavailableReason string
	internalAccountID string
	emptyRoute        string
	emptyProtocol     string
}

var wp2AccountPoolProfiles = []wp2AccountPoolProfileScenario{
	{
		accountProfileRef: "acct_11111111111111111111111111111111",
		status:            evidence.AccountPoolProfileEligible,
		internalAccountID: "wp2-synthetic-oid",
	},
	{
		accountProfileRef: "acct_22222222222222222222222222222222",
		status:            evidence.AccountPoolProfileEligible,
		internalAccountID: "wp2-pool-internal-b",
		emptyRoute:        "m365-gpt-5.6-think-deeper",
		emptyProtocol:     "openai_responses_nonstream",
	},
	{
		accountProfileRef: "acct_33333333333333333333333333333333",
		status:            evidence.AccountPoolProfileUnavailable,
		unavailableReason: "profile_not_ready",
	},
}

func BuildWP2AccountPoolEvidenceSet(options WP2AccountPoolHarnessOptions) (evidence.AccountPoolEvidenceSetV1, error) {
	routes, err := selectedWP2Routes(options.Routes)
	if err != nil {
		return evidence.AccountPoolEvidenceSetV1{}, err
	}
	adapters, err := selectedWP2RouteProtocolAdapters(options.Protocols)
	if err != nil {
		return evidence.AccountPoolEvidenceSetV1{}, err
	}
	profiles := make([]evidence.AccountPoolProfileInputV1, 0, len(wp2AccountPoolProfiles))
	for _, scenario := range wp2AccountPoolProfiles {
		profile := evidence.AccountPoolProfileInputV1{
			AccountProfileRef: scenario.accountProfileRef,
			Status:            scenario.status,
			UnavailableReason: scenario.unavailableReason,
		}
		if scenario.status == evidence.AccountPoolProfileUnavailable {
			profiles = append(profiles, profile)
			continue
		}
		profile.Matrix = make([]evidence.AccountPoolRouteProtocolInputV1, 0, len(routes)*len(adapters))
		for _, routeID := range routes {
			route, ok := builtInRoute(routeID)
			if !ok {
				return evidence.AccountPoolEvidenceSetV1{}, fmt.Errorf("account-pool route %q is not registered", routeID)
			}
			for _, adapter := range adapters {
				empty := route.ID == scenario.emptyRoute && adapter.protocol == scenario.emptyProtocol
				record, err := runWP2AccountPoolObservation(scenario, route, adapter, empty, options.Binding)
				if err != nil {
					return evidence.AccountPoolEvidenceSetV1{}, fmt.Errorf("profile %s %s %s: %w", scenario.accountProfileRef, route.ID, adapter.protocol, err)
				}
				capabilities := make([]evidence.AccountPoolCapabilityInputV1, 0, len(record.Capabilities))
				for _, capability := range record.Capabilities {
					capabilities = append(capabilities, evidence.AccountPoolCapabilityInputV1{
						CapabilityID:   capability.CapabilityID,
						Evidence:       append(json.RawMessage(nil), capability.CanonicalJSON...),
						EvidenceSHA256: capability.EvidenceSHA256,
					})
				}
				profile.Matrix = append(profile.Matrix, evidence.AccountPoolRouteProtocolInputV1{
					CanonicalRoute:      route.CanonicalRoute,
					ResolvedTone:        route.Tone,
					Protocol:            adapter.protocol,
					UpstreamAttempts:    record.Observation.UpstreamAttempts,
					CrossAccountResends: record.Observation.CrossAccountResends,
					Capabilities:        capabilities,
				})
			}
		}
		profiles = append(profiles, profile)
	}
	raw, err := json.Marshal(evidence.AccountPoolInputV1{Schema: evidence.AccountPoolInputSchemaV1, Profiles: profiles})
	if err != nil {
		return evidence.AccountPoolEvidenceSetV1{}, err
	}
	return evidence.BuildAccountPoolEvidence(raw)
}

func runWP2AccountPoolObservation(scenario wp2AccountPoolProfileScenario, route routeDefinition, adapter wp2RouteProtocolAdapter, upstreamEmpty bool, binding evidence.CaptureBinding) (evidence.RouteProtocolRecordV1, error) {
	result := chathub.Result{Text: "WP2_ACCOUNT_POOL_OK"}
	caseID := evidence.RouteProtocolCaseSuccess
	if upstreamEmpty {
		result = chathub.Result{}
		caseID = evidence.RouteProtocolCaseUpstreamEmpty
	}
	chat := &wp2HarnessChat{result: result}
	harness, cleanup, err := newWP2RouteProtocolHarnessServer(chat)
	if err != nil {
		return evidence.RouteProtocolRecordV1{}, err
	}
	defer cleanup()
	if _, err := harness.server.tokens.Upsert(auth.TokenSet{
		AccessToken: "wp2-pool-token-" + scenario.accountProfileRef,
		ExpiresAt:   time.Now().Add(time.Hour),
		Email:       scenario.accountProfileRef + "@example.test",
		HomeOID:     scenario.internalAccountID,
		TenantID:    "wp2-pool-tenant",
	}); err != nil {
		return evidence.RouteProtocolRecordV1{}, err
	}

	request, err := wp2AccountPoolRequest(adapter.buildRequest(route.ID), scenario.internalAccountID)
	if err != nil {
		return evidence.RouteProtocolRecordV1{}, err
	}
	writer := httptest.NewRecorder()
	harness.serve(adapter, writer, request)
	response := map[string]any{}
	_ = json.Unmarshal(writer.Body.Bytes(), &response)
	metadata, _ := response["m365"].(map[string]any)
	routeSwitches, crossAccountResends := wp2AttemptDivergence(chat.attempts, route.Tone)
	if len(chat.attempts) != 1 || chat.attempts[0].oid != scenario.internalAccountID {
		return evidence.RouteProtocolRecordV1{}, fmt.Errorf("selected account was not used exactly once")
	}

	canonicalRoute := route.CanonicalRoute
	resolvedTone := route.Tone
	topLevelModel := ""
	reasoningIgnored := false
	basicTextDelivered := false
	meaningfulEvent := "none"
	if !upstreamEmpty {
		if value, ok := metadata["canonical_route"].(string); ok {
			canonicalRoute = value
		}
		if value, ok := metadata["resolved_tone"].(string); ok {
			resolvedTone = value
		}
		topLevelModel, _ = response["model"].(string)
		reasoningIgnored, _ = metadata["reasoning_effort_ignored"].(bool)
		basicTextDelivered = strings.TrimSpace(adapter.decodeText(response)) != ""
		meaningfulEvent = "text"
	}
	capture := evidence.RouteProtocolCaptureV1{
		Schema:                  evidence.RouteProtocolCaptureSchemaV1,
		CaseID:                  caseID,
		Run:                     1,
		RequestedModel:          route.ID,
		TopLevelModel:           topLevelModel,
		CanonicalRoute:          canonicalRoute,
		ResolvedTone:            resolvedTone,
		Protocol:                adapter.protocol,
		EndpointPath:            adapter.path,
		AuthMode:                adapter.authMode,
		RequestIDObserved:       writer.Header().Get(requestIDHeader) != "",
		SecurityHeadersObserved: writer.Header().Get("X-Content-Type-Options") == "nosniff" && writer.Header().Get("X-Frame-Options") == "DENY",
		HTTPStatus:              writer.Code,
		BasicTextDelivered:      basicTextDelivered,
		ReasoningEffortApplied:  adapter.reasoningEffortApplied,
		ReasoningEffortIgnored:  reasoningIgnored,
		UpstreamAttempts:        len(chat.attempts),
		RouteSwitches:           routeSwitches,
		CrossAccountResends:     crossAccountResends,
		MeaningfulUpstreamEvent: meaningfulEvent,
		FailureCode:             routeProtocolFailureCode(response),
	}
	descriptor := evidence.RouteProtocolDescriptor{
		CanonicalRoute:  route.CanonicalRoute,
		ResolvedTone:    route.Tone,
		Protocol:        adapter.protocol,
		EndpointPath:    adapter.path,
		AuthMode:        adapter.authMode,
		MappingEvidence: "web_payload_verified",
		IdentityStatus:  string(route.IdentityStatus),
	}
	profileBinding := binding
	profileBinding.AccountProfileRef = scenario.accountProfileRef
	raw, err := json.Marshal(capture)
	if err != nil {
		return evidence.RouteProtocolRecordV1{}, err
	}
	return evidence.CaptureRouteProtocol(raw, descriptor, profileBinding)
}

func wp2AccountPoolRequest(request *http.Request, accountID string) (*http.Request, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	payload["accountId"] = accountID
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return httptest.NewRequest(request.Method, request.URL.Path, strings.NewReader(string(encoded))), nil
}
