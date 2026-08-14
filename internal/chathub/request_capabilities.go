package chathub

const defaultStreamingMode = "ConciseWithPadding"

var defaultOptionsSets = []string{
	"search_result_progress_messages_with_search_queries",
	"update_textdoc_response_after_streaming",
	"deepleo_networking_timeout_10minutes_canmore",
	"cwc_flux_image",
	"cwc_code_interpreter",
	"cwc_code_interpreter_amsfix",
	"cwcfluxgptv",
	"flux_v3_gptv_enable_upload_multi_image_in_turn_wo_ch",
	"gptvnorm2048",
	"cwc_code_interpreter_citation_fix",
	"code_interpreter_interactive_charts_inline_image",
	"code_interpreter_matplotlib_patching",
	"code_interpreter_interactive_charts",
	"cwc_fileupload_odb",
	"update_memory_plugin",
	"add_custom_instructions",
	"cwc_flux_v3",
	"flux_v3_progress_messages",
	"enable_batch_token_processing",
	"enable_gg_gpt",
}

var defaultAllowedMessageTypes = []string{
	"Chat", "Suggestion", "Disengaged", "Progress", "EndOfRequest", "InternalLoaderMessage",
	"GeneratedCode", "GenerateContentQuery", "ReferencesListComplete", "RenderCardRequest",
	"SearchQuery", "InternalSearchQuery", "SemanticSerp", "AuthError",
}

// RequestCapabilityBaseline is a privacy-safe snapshot of the static request
// capabilities the sidecar currently projects into ordinary ChatHub turns.
// It exists for diagnostics/evidence comparison only; callers cannot mutate the
// transport by changing the returned slices.
type RequestCapabilityBaseline struct {
	StreamingMode       string   `json:"streamingMode"`
	OptionsSets         []string `json:"optionsSets"`
	AllowedMessageTypes []string `json:"allowedMessageTypes"`
}

func CurrentRequestCapabilityBaseline() RequestCapabilityBaseline {
	return RequestCapabilityBaseline{
		StreamingMode:       defaultStreamingMode,
		OptionsSets:         append([]string(nil), defaultOptionsSets...),
		AllowedMessageTypes: append([]string(nil), defaultAllowedMessageTypes...),
	}
}

func defaultOptionsSetsAny() []any {
	out := make([]any, len(defaultOptionsSets))
	for i, option := range defaultOptionsSets {
		out[i] = option
	}
	return out
}
