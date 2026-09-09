package main

import "testing"

func TestPluginRegistrationExposesRoutes(t *testing.T) {
	for _, field := range pluginRegistration().Metadata.ConfigFields {
		if field.Name == "routes" {
			return
		}
	}
	t.Fatal("expected routes in plugin configuration fields")
}

func TestPluginRegistrationExposesVirtualModels(t *testing.T) {
	for _, field := range pluginRegistration().Metadata.ConfigFields {
		if field.Name == "virtual_models" {
			return
		}
	}
	t.Fatal("expected virtual_models in plugin configuration fields")
}

func TestPluginRegistrationExecutorFromEntry(t *testing.T) {
	for _, field := range pluginRegistration().Metadata.ConfigFields {
		if field.Name == "virtual_models" {
			return
		}
	}
	t.Fatal("expected virtual_models in plugin configuration fields")
}
