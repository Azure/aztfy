package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"flag"

	"github.com/Azure/aztfy/internal/config"
	"github.com/Azure/aztfy/internal/meta"
	"github.com/Azure/aztfy/internal/ui"
	"github.com/meowgorithm/babyenv"
)

var (
	flagSubscriptionId *string
	flagVersion        *bool
	flagOutputDir      *string
	flagMappingFile    *string
	flagContinue       *bool
	flagBatchMode      *bool
	flagPattern        *string
	flagOverwrite      *bool
	flagBackendType    *string
)

func init() {
	flagSubscriptionId = flag.String("s", "", "The subscription id")
	flagVersion = flag.Bool("v", false, "Print version")
	flagOutputDir = flag.String("o", "", "Specify output dir. Default is the current working directory")
	flagMappingFile = flag.String("m", "", "Specify the resource mapping file")
	flagContinue = flag.Bool("k", false, "Whether continue on import error (batch mode only)")
	flagBatchMode = flag.Bool("b", false, "Batch mode (i.e. Non-interactive mode)")
	flagPattern = flag.String("p", "res-", `The pattern of the resource name. The resource name is generated by taking the pattern and adding an auto-incremental integer to the end. If pattern includes a "*", the auto-incremental integer replaces the last "*"`)
	flagOverwrite = flag.Bool("f", false, "Whether to overwrite the out dir if it is not empty, use with caution")
	flagBackendType = flag.String("backend-type", "local", "The Terraform backend used to store the state")
}

const usage = `aztfy [option] <resource group name>
`

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

type strSliceFlag struct {
	values *[]string
}

func (o *strSliceFlag) String() string { return "" }
func (o *strSliceFlag) Set(val string) error {
	*o.values = append(*o.values, val)
	return nil
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "%s\n", usage)
		flag.PrintDefaults()
	}

	var backendConfig []string
	flag.Var(&strSliceFlag{
		values: &backendConfig,
	}, "backend-config", "The Terraform backend config")

	flag.Parse()

	if *flagVersion {
		if revision != "" {
			fmt.Fprintf(flag.CommandLine.Output(), "%s(%s)\n", version, revision)
		} else {
			fmt.Println(version)
		}
		os.Exit(0)
	}

	// Flag sanity check
	if len(flag.Args()) != 1 {
		flag.Usage()
		os.Exit(1)
	}
	if *flagBatchMode && *flagMappingFile == "" {
		fatal(errors.New("`-q` must be used together with `-m`"))
	}
	if *flagContinue && !*flagBatchMode {
		fatal(errors.New("`-k` must be used together with `-q`"))
	}

	rg := flag.Args()[0]

	// Initialize the config
	var cfg config.Config
	if err := babyenv.Parse(&cfg); err != nil {
		fatal(err)
	}

	// The subscription id comes from one of following (starts from the highest priority):
	// - Command line option
	// - Env variable: AZTFY_SUBSCRIPTION_ID
	// - Env variable: ARM_SUBSCRIPTION_ID
	// - Output of azure cli, the current active subscription
	if *flagSubscriptionId != "" {
		cfg.SubscriptionId = *flagSubscriptionId
	}
	if cfg.SubscriptionId == "" {
		// Honor the ARM_SUBSCRIPTION_ID as the AzureRM provider does.
		if v := os.Getenv("ARM_SUBSCRIPTION_ID"); v != "" {
			cfg.SubscriptionId = v
		} else {
			var stderr bytes.Buffer
			var stdout bytes.Buffer
			cmd := exec.Command("az", "account", "show", "--query", "id")
			cmd.Stderr = &stderr
			cmd.Stdout = &stdout
			if err := cmd.Run(); err != nil {
				err = fmt.Errorf("failed to run azure cli: %v", err)
				if stdErrStr := stderr.String(); stdErrStr != "" {
					err = fmt.Errorf("%s: %s", err, strings.TrimSpace(stdErrStr))
				}
				fatal(err)
			}
			if stdout.String() == "" {
				fatal(fmt.Errorf("subscription id is not specified"))
			}
			subid, err := strconv.Unquote(strings.TrimSpace(stdout.String()))
			if err != nil {
				fatal(fmt.Errorf("unquoting %s: %v", stdout.String(), err))
			}
			cfg.SubscriptionId = subid
		}
	}

	cfg.ResourceGroupName = rg
	cfg.ResourceNamePattern = *flagPattern
	cfg.ResourceMappingFile = *flagMappingFile
	cfg.OutputDir = *flagOutputDir
	cfg.Overwrite = *flagOverwrite
	cfg.BatchMode = *flagBatchMode
	cfg.BackendType = *flagBackendType
	cfg.BackendConfig = backendConfig

	if cfg.BatchMode {
		if err := batchImport(cfg, *flagContinue); err != nil {
			fatal(err)
		}
		return
	}

	prog, err := ui.NewProgram(cfg)
	if err != nil {
		fatal(err)
	}

	if err := prog.Start(); err != nil {
		fatal(err)
	}
}

func batchImport(cfg config.Config, continueOnError bool) error {
	// Discard logs from hashicorp/azure-go-helper
	log.SetOutput(io.Discard)
	// Define another dedicated logger for the ui
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if cfg.Logfile != "" {
		f, err := os.OpenFile(cfg.Logfile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			return err
		}
		logger = log.New(f, "aztfy", log.LstdFlags)
	}

	logger.Println("New meta")
	c, err := meta.NewMeta(cfg)
	if err != nil {
		return err
	}

	logger.Println("Initialize")
	if err := c.Init(); err != nil {
		return err
	}

	logger.Println("List resources")
	list := c.ListResource()

	logger.Println("Import resources")
	for i := range list {
		if list[i].Skip() {
			logger.Printf("[WARN] No mapping information for resource: %s, skip it\n", list[i].ResourceID)
			continue
		}
		logger.Printf("Importing %s as %s\n", list[i].ResourceID, list[i].TFAddr)
		c.Import(&list[i])
		if err := list[i].ImportError; err != nil {
			msg := fmt.Sprintf("Failed to import %s as %s: %v", list[i].ResourceID, list[i].TFAddr, err)
			if !continueOnError {
				return fmt.Errorf(msg)
			}
			logger.Println("[ERROR] " + msg)
		}
	}

	logger.Println("Generate Terraform configurations")
	if err := c.GenerateCfg(list); err != nil {
		return fmt.Errorf("generating Terraform configuration: %v", err)
	}

	return nil
}
