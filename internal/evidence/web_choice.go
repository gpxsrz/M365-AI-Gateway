package evidence

const (
	WebChoiceCaptureSchemaV1     = "m365-wp2-web-choice-capture/v1"
	WebChoiceObservationSchemaV1 = "m365-wp2-web-choice-observation/v1"
	MaxWebChoiceCaptureBytes     = 4 * 1024
)

type MappingBehavior string

const (
	MappingBehaviorExact          MappingBehavior = "exact"
	MappingBehaviorCaseNormalized MappingBehavior = "case_normalized"
	MappingBehaviorDifferent      MappingBehavior = "different"
)

type WebChoiceRoute struct {
	WebChoiceID    string
	CanonicalRoute string
	RegistryTone   string
	RouteKind      string
	IdentityStatus string
}

type WebChoiceObservationV1 struct {
	Schema          string          `json:"schema"`
	WebChoiceID     string          `json:"web_choice_id"`
	CanonicalRoute  string          `json:"canonical_route"`
	RouteKind       string          `json:"route_kind"`
	RegistryTone    string          `json:"registry_tone"`
	ObservedWebTone string          `json:"observed_web_tone"`
	MappingBehavior MappingBehavior `json:"mapping_behavior"`
}

type CapturedWebChoice struct {
	Observation              WebChoiceObservationV1
	ObservationCanonicalJSON []byte
	ObservationSHA256        string
	Evidence                 ValidatedRecord
}
