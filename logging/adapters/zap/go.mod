module github.com/smplkit/go-sdk/logging/adapters/zap

go 1.24.3

require (
	github.com/smplkit/go-sdk/v3 v3.0.0
	github.com/stretchr/testify v1.12.0
	go.uber.org/zap v1.28.0
)

require (
	go.uber.org/multierr v1.10.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/smplkit/go-sdk/v3 => ../../..
