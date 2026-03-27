package provider

// mergeExtraRequestOptions copies user-supplied request options into the final
// provider request map while protecting the core reserved fields built by the
// runtime itself.
//
// Why:
// Profiles need a narrow escape hatch for provider-specific parameters such as
// reasoning effort or other vendor extensions. At the same time, we do not
// want arbitrary profile data to overwrite the core request shape such as
// model, messages, tools, or stream flags.
func mergeExtraRequestOptions(dst map[string]any, options map[string]any, reserved map[string]struct{}) {
	if len(options) == 0 {
		return
	}
	for key, value := range options {
		if _, blocked := reserved[key]; blocked {
			continue
		}
		dst[key] = value
	}
}
