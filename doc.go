// tailport shares a local port with your tailnet, and optionally the public
// internet, using its own embedded Tailscale node named "tailport".
//
// Install it with:
//
//	go install github.com/MandavkarPranjal/tailport@latest
//
// then run "tailport" to pick a port from a menu, or "tailport up -p 3000" to
// name one outright. Inside your tailnet the service is at http://tailport; on
// the public internet it is at https://tailport.<your-tailnet>.ts.net.
//
// Each machine keeps its own node credentials under its user config directory,
// so the first run asks you to log in through a browser and later runs start
// straight away. Set TS_AUTHKEY (or -authkey) to skip the browser entirely.
package tailport
