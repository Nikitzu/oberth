package main

import (
	"github.com/oberthci/oberth/internal/client"
	"github.com/oberthci/oberth/internal/clientprofile"
)

func clientConfig() client.Config { return clientConfigFor(".") }

func clientConfigFor(dir string) client.Config {
	if name := clientprofile.ForCheckout(dir); name != "" {
		if profile, err := clientprofile.Load(name); err == nil {
			return client.Config{BaseURL: profile.BaseURL, CACert: profile.CACert, TokenCommand: profile.TokenCommand}
		}
	}
	return client.FromEnv()
}
