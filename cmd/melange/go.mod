module github.com/pthm/melange/cmd/melange

go 1.24.0

retract (
	v0.3.0 // Published when cmd/melange was a separate module with replace directives
	v0.2.0 // Published when cmd/melange was a separate module with replace directives
	v0.1.0 // Published when cmd/melange was a separate module with replace directives
)

require (
	github.com/lib/pq v1.10.9
	github.com/pthm/melange v0.7.2
	github.com/spf13/cobra v1.10.2
	sigs.k8s.io/yaml v1.6.0
)

require (
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/envoyproxy/protoc-gen-validate v1.2.1 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.4 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/openfga/api/proto v0.0.0-20260122164422-25e22cb1875b // indirect
	github.com/openfga/language/pkg/go v0.2.0-beta.2.0.20251027165255-0f8f255e5f6c // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/pthm/melange/melange v0.7.1 // indirect
	github.com/sagikazarmark/locafero v0.9.0 // indirect
	github.com/sourcegraph/conc v0.3.0 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/spf13/viper v1.20.1 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/exp v0.0.0-20251023183803-a4bb9ffd2546 // indirect
	golang.org/x/net v0.48.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20251222181119-0a764e51fe1b // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251222181119-0a764e51fe1b // indirect
	google.golang.org/grpc v1.78.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
