package caddyslicervm

import (
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func init() {
	caddy.RegisterModule(SlicerVM{})
}

// Interface guards
var (
	_ caddy.Provisioner           = (*SlicerVM)(nil)
	_ caddy.Validator             = (*SlicerVM)(nil)
	_ caddy.CleanerUpper          = (*SlicerVM)(nil)
	_ caddyhttp.MiddlewareHandler = (*SlicerVM)(nil)
	_ caddyfile.Unmarshaler       = (*SlicerVM)(nil)
)

// CaddyModule returns the Caddy module information.
func (SlicerVM) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.slicervm",
		New: func() caddy.Module { return new(SlicerVM) },
	}
}
