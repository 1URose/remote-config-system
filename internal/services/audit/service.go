package audit

const MaskedValue = "****"

func MaskValue(value string, isSecret bool) string {
	if !isSecret {
		return value
	}
	if value == "" {
		return ""
	}
	return MaskedValue
}
