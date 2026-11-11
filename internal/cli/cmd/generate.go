package cmd

import (
	"io"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var generateCmd = &cobra.Command{
	Use:     "generate",
	Aliases: []string{"gen"},
	Short:   "Generate Scorix application code",
}

var generateProtoCmd = &cobra.Command{
	Use:     "proto",
	Aliases: []string{"rpc"},
	Short:   "Generate handler, logic and types from a proto file",
}

var (
	generateProtoFile  string
	generateProtoDir   string
	generateProtoForce bool
	generateProtoCheck bool
)

func init() {
	jsonCommand(generateProtoCmd, func(cmd *cobra.Command, out io.Writer) error {
		return runner.GenerateProto(cmd.Context(), runner.GenerateProtoOptions{
			JSONOut: out,
			Proto:   generateProtoFile,
			Dir:     generateProtoDir,
			Force:   generateProtoForce,
			Check:   generateProtoCheck,
		})
	})
	rootCmd.AddCommand(generateCmd)
	generateCmd.AddCommand(generateProtoCmd)

	generateProtoCmd.Flags().StringVarP(&generateProtoFile, "proto", "p", runner.DefaultProtoPath, "proto file path; an explicit value wins over scorix.yaml proto:")
	generateProtoCmd.Flags().StringVarP(&generateProtoDir, "dir", "d", ".", "project root directory")
	generateProtoCmd.Flags().BoolVarP(&generateProtoForce, "force", "f", false, "overwrite existing logic files")
	generateProtoCmd.Flags().BoolVar(&generateProtoCheck, "check", false, "verify generated code is in sync without writing (CI drift guard)")
}

var generateServerCmd = &cobra.Command{
	Use:   "server",
	Short: "Generate the Connect and gRPC routes of a network service from its proto",
}

var generateServerOpt runner.GenerateServerOptions

func init() {
	jsonCommand(generateServerCmd, func(cmd *cobra.Command, out io.Writer) error {
		opt := generateServerOpt
		opt.JSONOut = out
		return runner.GenerateServer(cmd.Context(), opt)
	})
	generateCmd.AddCommand(generateServerCmd)
	generateServerCmd.Flags().StringVarP(&generateServerOpt.Proto, "proto", "p", "", "proto file of the service")
	generateServerCmd.Flags().StringVar(&generateServerOpt.PB, "pb", "", "Go import path of the protoc-gen-go package for that proto")
	generateServerCmd.Flags().StringVarP(&generateServerOpt.Out, "out", "o", "", "Go file to write; its directory names the package")
	generateServerCmd.Flags().BoolVar(&generateServerOpt.Check, "check", false, "verify the file is in sync without writing (CI drift guard)")
}

var generateOpenAPICmd = &cobra.Command{
	Use:   "openapi",
	Short: "Generate the OpenAPI document of a service's REST routes from its proto",
}

var generateOpenAPIOpt runner.GenerateOpenAPIOptions

func init() {
	jsonCommand(generateOpenAPICmd, func(cmd *cobra.Command, out io.Writer) error {
		opt := generateOpenAPIOpt
		opt.JSONOut = out
		return runner.GenerateOpenAPI(cmd.Context(), opt)
	})
	generateCmd.AddCommand(generateOpenAPICmd)
	generateOpenAPICmd.Flags().StringVarP(&generateOpenAPIOpt.Proto, "proto", "p", "", "proto file of the service")
	generateOpenAPICmd.Flags().StringVarP(&generateOpenAPIOpt.Out, "out", "o", "", "JSON file to write")
	generateOpenAPICmd.Flags().StringVar(&generateOpenAPIOpt.Title, "title", "", "info.title; the proto package when empty")
	generateOpenAPICmd.Flags().StringVar(&generateOpenAPIOpt.Version, "version", "", "info.version")
	generateOpenAPICmd.Flags().BoolVar(&generateOpenAPIOpt.Check, "check", false, "verify the file is in sync without writing (CI drift guard)")
}

var generateDispatchCmd = &cobra.Command{
	Use:   "dispatch",
	Short: "Generate the server that hands each rpc to its logic type, in place of goctl's",
}

var generateDispatchOpt runner.GenerateDispatchOptions

func init() {
	jsonCommand(generateDispatchCmd, func(cmd *cobra.Command, out io.Writer) error {
		opt := generateDispatchOpt
		opt.JSONOut = out
		return runner.GenerateDispatch(cmd.Context(), opt)
	})
	generateCmd.AddCommand(generateDispatchCmd)
	generateDispatchCmd.Flags().StringVarP(&generateDispatchOpt.Proto, "proto", "p", "", "proto file of the service")
	generateDispatchCmd.Flags().StringVar(&generateDispatchOpt.PB, "pb", "", "Go import path of the protoc-gen-go package for that proto")
	generateDispatchCmd.Flags().StringVar(&generateDispatchOpt.Logic, "logic", "", "Go import path of the package with New<Rpc>Logic")
	generateDispatchCmd.Flags().StringVar(&generateDispatchOpt.Svc, "svc", "", "Go import path of the package with ServiceContext")
	generateDispatchCmd.Flags().StringVarP(&generateDispatchOpt.Out, "out", "o", "", "Go file to write; its directory names the package")
	generateDispatchCmd.Flags().BoolVar(&generateDispatchOpt.Check, "check", false, "verify the file is in sync without writing (CI drift guard)")
}
