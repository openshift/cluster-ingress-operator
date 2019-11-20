package aws

import (
	"bytes"
	"io/ioutil"
	"time"

	certsutil "github.com/openshift/cluster-ingress-operator/pkg/util/certs"

	"k8s.io/apimachinery/pkg/util/wait"
)

// startWatcher starts the FileWatcher and periodically ensures DNS clients
// are using the current ca bundle until a message is received on the stop
// or error channels.
func (m *Provider) startWatcher(stop <-chan struct{}) error {
	go wait.Until(func() {
		if err := m.ensureDNSClientsTLSTransport(); err != nil {
			log.Error(err, "failed to ensure dns client tls transport")
		}
	}, 1*time.Minute, stop)

	errChan := make(chan error)
	go func() {
		errChan <- m.fileWatcher.Start(stop)
	}()

	// Wait for the watcher to exit or an explicit stop.
	select {
	case <-stop:
		return nil
	case err := <-errChan:
		return err
	}
}

// ensureDNSClientsTLSTransport compares the watched ca bundle with the
// current ca bundle of FileWatcher and updates DNS clients with the current
// ca bundle if the two are not equal.
func (m *Provider) ensureDNSClientsTLSTransport() error {
	equal, caBundle, err := m.caBundlesEqual()
	if err != nil {
		return err
	}
	if equal {
		return nil
	}

	certs, err := certsutil.CertsFromPEM(caBundle)
	if err != nil {
		return err
	}

	m.route53.Client.Config.HTTPClient.Transport = certsutil.MakeTLSTransport(certs)
	m.elb.Client.Config.HTTPClient.Transport = certsutil.MakeTLSTransport(certs)
	m.tags.Client.Config.HTTPClient.Transport = certsutil.MakeTLSTransport(certs)

	return nil
}

// caBundlesEqual compares the watched ca bundle with the current
// ca bundle of FileWatcher, returning false and the current ca
// bundle if the two are not equal.
func (m *Provider) caBundlesEqual() (bool, []byte, error) {
	watchedCAs, err := ioutil.ReadFile(m.fileWatcher.GetFile())
	if err != nil {
		return false, nil, err
	}

	currentCAs := m.fileWatcher.GetFileData()
	if !bytes.Equal(watchedCAs, currentCAs) {
		return false, currentCAs, nil
	}

	return true, nil, nil
}
