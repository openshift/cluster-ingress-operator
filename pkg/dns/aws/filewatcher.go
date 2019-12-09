package aws

import (
	"fmt"
)

// StartWatcher starts a file watcher and ensures AWS sessions are using the
// current ca bundle until a message is received on the stop or error channels.
func (m *Provider) StartWatcher(operatorReleaseVersion string, stop <-chan struct{}) error {
	errChan := make(chan error)
	reloadChan := make(chan struct{})
	go func() {
		errChan <- m.fileWatcher.Start(stop, reloadChan)
	}()
	go func() {
		errChan <- m.startSessionReloadHandler(operatorReleaseVersion, reloadChan)
	}()

	// Wait for the watcher to exit or an explicit stop.
	select {
	case <-stop:
		return nil
	case err := <-errChan:
		return err
	}
}

// startSessionReloadHandler creates new route53, elb and resourcegroupstagging
// api client sessions when a value is received on reloadCh.
func (m *Provider) startSessionReloadHandler(operatorReleaseVersion string, reloadCh chan struct{}) error {
	for {
		select {
		case <-reloadCh:
			sess, err := newProviderSession(m.config, operatorReleaseVersion, m.fileWatcher.GetFileData())
			if err != nil {
				return fmt.Errorf("failed to create dns provider session: %v", err)
			}
			m.lock.Lock()
			elbClient, route53Client, taggingClient := newClients(sess, m.config.Region)
			m.elb = elbClient
			m.route53 = route53Client
			m.tags = taggingClient
			m.lock.Unlock()
			log.Info("recreated aws dns provider sessions")
		}
	}
}
