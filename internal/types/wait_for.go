package types

type WaitFor struct {
	Field []WaitForField
}
type WaitForField struct {
	Key       string
	Value     string
	ValueType string `mapstructure:"value_type"`
}
