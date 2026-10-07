"""Apply the company profile to exactly the pinned upstream registration site."""

from pathlib import Path

p = Path("cmd/ocis-mcp-server/main.go")
source = p.read_text()
old_config = "\tcfg, err := config.Load()"
assert source.count(old_config) == 1, "upstream configuration changed"
source = source.replace(
    old_config,
    """\tif err := tools.ConfigureCompany(); err != nil {
        slog.Error("company MCP profile invalid", "error", err)
        os.Exit(1)
    }
"""
    + old_config,
)
old = "\ttools.RegisterAll(server, ocisClient, cfg)"
assert source.count(old) == 1, "upstream registration changed"
p.write_text(
    source.replace(
        old,
        """\tif err := tools.RegisterCompany(server, ocisClient, cfg); err != nil {
        slog.Error("company MCP configuration invalid", "error", err)
        os.Exit(1)
    }""",
    )
)

# Discovery must not forward credentials to redirect destinations either.
p = Path("internal/config/config.go")
source = p.read_text()
old = "\t\tTransport: transport,"
assert source.count(old) == 1, "upstream HTTP client changed"
source = source.replace(
    old,
    old
    + "\n\t\tCheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },",
)
source = source.replace('"crypto/tls"', '"context"\n"crypto/tls"', 1)
old = "\treturn &http.Client{"
assert source.count(old) == 1, "upstream HTTP construction changed"
source = source.replace(
    old,
    """
    // Keep the canonical HTTPS Host/SNI while dialing an internal ingress Service.
    // This changes only TCP routing; certificate verification is still mandatory.
    if address := os.Getenv("OCIS_MCP_CONNECT_ADDRESS"); address != "" {
        transport.Proxy = nil
        origin, parseErr := url.Parse(c.OcisBaseURL())
        expected := ""
        if parseErr == nil {
            expected = origin.Host
            if origin.Port() == "" { expected = net.JoinHostPort(origin.Hostname(), "443") }
        }
        transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
            if parseErr != nil || addr != expected { return nil, fmt.Errorf("unexpected oCIS connection origin") }
            host, port, err := net.SplitHostPort(address)
            if err != nil || host == "" || port == "" { return nil, fmt.Errorf("invalid internal oCIS connection address") }
            return (&net.Dialer{Timeout: 10*time.Second}).DialContext(ctx, network, address)
        }
    }
"""
    + old,
    1,
)
p.write_text(source)
