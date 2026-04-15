package handlers

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/averyhabbott/netbox-conductor/deployments/agent"
	"github.com/labstack/echo/v4"
)

// DownloadHandler serves agent tarballs and the TLS CA certificate.
type DownloadHandler struct {
	binDir   string
	certFile string // path to the PEM cert served as ca.crt
}

func NewDownloadHandler(binDir, certFile string) *DownloadHandler {
	return &DownloadHandler{binDir: binDir, certFile: certFile}
}

// AgentBinary streams a .tar.gz containing the agent binary and all support
// files needed for a first-time install (env example, systemd service, install script).
// GET /api/v1/downloads/agent-linux-{arch}
func (h *DownloadHandler) AgentBinary(arch string) echo.HandlerFunc {
	return func(c echo.Context) error {
		binName := "netbox-agent-linux-" + arch
		binPath := filepath.Join(h.binDir, binName)

		binData, err := os.ReadFile(binPath)
		if err != nil {
			if os.IsNotExist(err) {
				return echo.NewHTTPError(http.StatusNotFound,
					fmt.Sprintf("agent binary for %s not found on this conductor", arch))
			}
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to read agent binary")
		}

		tarball := binName + ".tar.gz"
		c.Response().Header().Set("Content-Type", "application/gzip")
		c.Response().Header().Set("Content-Disposition", "attachment; filename="+tarball)
		c.Response().WriteHeader(http.StatusOK)

		gz := gzip.NewWriter(c.Response().Writer)
		tw := tar.NewWriter(gz)

		type entry struct {
			name string
			mode int64
			data []byte
		}
		files := []entry{
			{"netbox-agent", 0755, binData},
			{"netbox-agent.env.example", 0644, agentbundle.EnvExample},
			{"netbox-agent.service", 0644, agentbundle.ServiceFile},
			{"netbox-agent-sudoers", 0440, agentbundle.SudoersFile},
			{"install.sh", 0755, agentbundle.InstallScript},
		}

		for _, f := range files {
			hdr := &tar.Header{
				Name: f.name,
				Mode: f.mode,
				Size: int64(len(f.data)),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if _, err := tw.Write(f.data); err != nil {
				return err
			}
		}

		_ = tw.Close()
		_ = gz.Close()
		return nil
	}
}

// CACert serves the conductor's TLS certificate as ca.crt so agents can trust it.
// GET /api/v1/downloads/ca.crt
func (h *DownloadHandler) CACert(c echo.Context) error {
	if h.certFile == "" {
		return echo.NewHTTPError(http.StatusNotFound, "TLS is not enabled on this conductor")
	}
	if _, err := os.Stat(h.certFile); os.IsNotExist(err) {
		return echo.NewHTTPError(http.StatusNotFound, "TLS cert not found")
	}
	c.Response().Header().Set("Content-Type", "application/x-pem-file")
	c.Response().Header().Set("Content-Disposition", "attachment; filename=ca.crt")
	return c.File(h.certFile)
}
