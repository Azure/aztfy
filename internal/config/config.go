package config

import (
	"github.com/Azure/aztfy/internal/resmap"
)

type Config interface {
	isConfig()
}

type CommonConfig struct {
	SubscriptionId string
	OutputDir      string
	Overwrite      bool
	Append         bool
	DevProvider    bool
	BatchMode      bool
	BackendType    string
	BackendConfig  []string
}

type RgConfig struct {
	CommonConfig

	ResourceGroupName   string
	ResourceMapping     resmap.ResourceMapping
	ResourceNamePattern string
	MockClient          bool
}

func (RgConfig) isConfig() {}

type ResConfig struct {
	CommonConfig

	// Azure resource id
	ResourceId string

	// TF resource name
	ResourceName string
}

func (ResConfig) isConfig() {}
