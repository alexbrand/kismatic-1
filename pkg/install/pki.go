package install

import (
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"

	"github.com/apprenda/kismatic-platform/pkg/tls"
	"github.com/cloudflare/cfssl/csr"
)

// The PKI provides a way for generating certificates for the cluster described by the Plan
type PKI interface {
	GenerateClusterCerts(p *Plan) error
}

// LocalPKI is a file-based PKI
type LocalPKI struct {
	CACsr            string
	CAConfigFile     string
	CASigningProfile string
	DestinationDir   string
	Log              io.Writer
}

// GenerateClusterCerts creates a Certificate Authority and Certificates
// for all nodes on the cluster.
func (lp *LocalPKI) GenerateClusterCerts(p *Plan) error {
	if lp.Log == nil {
		lp.Log = ioutil.Discard
	}
	// First, generate a CA
	key, cert, err := tls.NewCACert(lp.CACsr)
	if err != nil {
		return fmt.Errorf("failed to create CA Cert: %v", err)
	}

	err = lp.writeFiles(key, cert, "ca")
	if err != nil {
		return fmt.Errorf("error writing CA files: %v", err)
	}

	ca := &tls.CA{
		Key:        key,
		Cert:       cert,
		ConfigFile: lp.CAConfigFile,
		Profile:    lp.CASigningProfile,
	}

	// Add kubernetes service IP (first IP in service CIDR)
	_, servNet, err := net.ParseCIDR(p.Cluster.Networking.ServiceCIDRBlock)
	if err != nil {
		return fmt.Errorf("error parsing Service CIDR block %q: %v", p.Cluster.Networking.ServiceCIDRBlock, err)
	}
	kubeServiceIP := servNet.IP.To4()
	kubeServiceIP[3]++

	defaultCertHosts := []string{
		"kubernetes",
		"kubernetes.default",
		"kubernetes.default.svc",
		"kubernetes.default.svc.cluster.local",
		"10.3.0.10",
		"127.0.0.1",
		kubeServiceIP.String(),
	}

	// Then, create certs for all nodes
	nodes := []Node{}
	nodes = append(nodes, p.Etcd.Nodes...)
	nodes = append(nodes, p.Master.Nodes...)
	nodes = append(nodes, p.Worker.Nodes...)

	for _, n := range nodes {
		fmt.Fprintf(lp.Log, "Generating certificates for %q\n", n.Host)
		key, cert, err := generateNodeCert(p, &n, ca, defaultCertHosts)
		if err != nil {
			return fmt.Errorf("error during cluster cert generation: %v", err)
		}
		err = lp.writeFiles(key, cert, n.Host)
		if err != nil {
			return fmt.Errorf("error writing cert files for host %q: %v", n.Host, err)
		}
	}
	return nil
}

func (lp *LocalPKI) writeFiles(key, cert []byte, name string) error {
	// Create destination dir if it doesn't exist
	if _, err := os.Stat(lp.DestinationDir); os.IsNotExist(err) {
		err := os.Mkdir(lp.DestinationDir, 0744)
		if err != nil {
			return fmt.Errorf("error creating destination dir: %v", err)
		}
	}

	// Write private key with read-only for user
	keyName := fmt.Sprintf("%s-key.pem", name)
	dest := filepath.Join(lp.DestinationDir, keyName)
	err := ioutil.WriteFile(dest, key, 0600)
	if err != nil {
		return fmt.Errorf("error writing private key: %v", err)
	}

	// Write cert
	certName := fmt.Sprintf("%s.pem", name)
	dest = filepath.Join(lp.DestinationDir, certName)
	err = ioutil.WriteFile(dest, cert, 0644)
	if err != nil {
		return fmt.Errorf("error writing certificate: %v", err)
	}
	return nil
}

func generateNodeCert(p *Plan, n *Node, ca *tls.CA, initialHostList []string) (key, cert []byte, err error) {
	hosts := append(initialHostList, n.Host, n.InternalIP, n.IP)
	req := csr.CertificateRequest{
		CN: p.Cluster.Name,
		KeyRequest: &csr.BasicKeyRequest{
			A: "rsa",
			S: 2048,
		},
		Hosts: hosts,
		Names: []csr.Name{
			{
				C:  p.Cluster.Certificates.LocationCountry,
				ST: p.Cluster.Certificates.LocationState,
				L:  p.Cluster.Certificates.LocationCity,
			},
		},
	}

	key, cert, err = tls.GenerateNewCertificate(ca, req)
	if err != nil {
		return nil, nil, fmt.Errorf("error generating certs for node %q: %v", n.Host, err)
	}

	return key, cert, err
}