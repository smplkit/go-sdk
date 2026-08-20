module github.com/smplkit/go-sdk/logging/adapters/slog

go 1.24.3

require (
	github.com/smplkit/go-sdk/v3 v3.0.0
	github.com/stretchr/testify v1.12.0
)

require gopkg.in/yaml.v3 v3.0.1 // indirect

replace github.com/smplkit/go-sdk/v3 => ../../..
