package caddyslicervm

import (
	"strconv"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func init() {
	httpcaddyfile.RegisterHandlerDirective("slicervm", parseCaddyfile)
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler.
//
//	slicervm {
//	    slicer_url     <url or socket path>
//	    slicer_token   <token>
//	    host_group     <name>
//	    idle_timeout   <duration>
//	    wake_timeout   <duration>
//	    app_port       <port>
//	    watch_interval <duration>
//	}
func (rs *SlicerVM) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // consume directive name

	for d.NextBlock(0) {
		switch d.Val() {
		case "slicer_url":
			if !d.NextArg() {
				return d.ArgErr()
			}
			rs.SlicerURL = d.Val()

		case "slicer_token":
			if !d.NextArg() {
				return d.ArgErr()
			}
			rs.SlicerToken = d.Val()

		case "host_group":
			if !d.NextArg() {
				return d.ArgErr()
			}
			rs.HostGroup = d.Val()

		case "idle_timeout":
			if !d.NextArg() {
				return d.ArgErr()
			}
			dur, err := time.ParseDuration(d.Val())
			if err != nil {
				return d.Errf("parsing idle_timeout: %v", err)
			}
			rs.IdleTimeout = caddy.Duration(dur)

		case "wake_timeout":
			if !d.NextArg() {
				return d.ArgErr()
			}
			dur, err := time.ParseDuration(d.Val())
			if err != nil {
				return d.Errf("parsing wake_timeout: %v", err)
			}
			rs.WakeTimeout = caddy.Duration(dur)

		case "app_port":
			if !d.NextArg() {
				return d.ArgErr()
			}
			port, err := strconv.Atoi(d.Val())
			if err != nil {
				return d.Errf("parsing app_port: %v", err)
			}
			rs.AppPort = port

		case "watch_interval":
			if !d.NextArg() {
				return d.ArgErr()
			}
			dur, err := time.ParseDuration(d.Val())
			if err != nil {
				return d.Errf("parsing watch_interval: %v", err)
			}
			rs.WatchInterval = caddy.Duration(dur)

		default:
			return d.Errf("unknown subdirective: %s", d.Val())
		}
	}

	return nil
}

// parseCaddyfile sets up the handler from Caddyfile tokens.
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var rs SlicerVM
	if err := rs.UnmarshalCaddyfile(h.Dispenser); err != nil {
		return nil, err
	}
	return &rs, nil
}
