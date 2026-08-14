package evidence

// CaptureBinding is shared evidence identity data. The runtime consumes the
// resulting committed evidence models, while offline builders use this binding
// to create those artifacts.
type CaptureBinding struct {
	NormativeADRSHA256      string
	SourceHead              string
	DirtyContentSHA256      string
	BinarySHA256            string
	HarnessSHA256           string
	AccountProfileRef       string
	EffectiveSettingsSHA256 string
}
