// Package prifi-sda-service contains the SDA service responsible
// for starting the SDA protocols required to enable PriFi
// communications.
package prifi

/*
* This is the internal part of the API. As probably the prifi-service will
* not have an external API, this will not have any API-functions.
 */

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"

	"github.com/dedis/cothority/app/lib/config"
	"github.com/dedis/cothority/log"
	"github.com/dedis/cothority/network"
	"github.com/dedis/cothority/sda"
	"github.com/lbarman/prifi_dev/sda/protocols"
)

// ServiceName is the name to refer to the Template service from another
// package.
const ServiceName = "ExampleService"

var serviceID sda.ServiceID

// Register Service with SDA
func init() {
	sda.RegisterNewService(ServiceName, newService)
	serviceID = sda.ServiceFactory.ServiceID(ServiceName)
}

// Service contains the state of the service
type Service struct {
	// We need to embed the ServiceProcessor, so that incoming messages
	// are correctly handled.
	*sda.ServiceProcessor
	group   *config.Group
	Storage *Storage
	path    string
	role prifi.PriFiRole
	identityMap map[network.Address]prifi.PriFiIdentity
	relayIdentity *network.ServerIdentity
	waitQueue *waitQueue
	prifiWrapper *prifi.PriFiSDAWrapper
	isPrifiRunning bool
}

// Storage will be saved, on the contrary of the 'Service'-structure
// which has per-service information stored.
type Storage struct {
	TrusteeID string
}

// StartTrustee starts the necessary
// protocols to enable the trustee-mode.
func (s *Service) StartTrustee(group *config.Group) error {
	log.Info("Service", s, "running in trustee mode")
	s.role = prifi.Trustee
	s.readGroup(group)

	s.autoConnect()

	return nil
}

// StartRelay starts the necessary
// protocols to enable the relay-mode.
// In this example it simply starts the demo protocol
func (s *Service) StartRelay(group *config.Group) error {
	log.Info("Service", s, "running in relay mode")
	s.role = prifi.Relay
	s.readGroup(group)
	s.waitQueue = &waitQueue{
		clients: make(map[*network.ServerIdentity]bool),
		trustees: make(map[*network.ServerIdentity]bool),
	}

	return nil
}

// StartClient starts the necessary
// protocols to enable the client-mode.
func (s *Service) StartClient(group *config.Group) error {
	log.Info("Service", s, "running in client mode")
	s.role = prifi.Client
	s.readGroup(group)

	s.autoConnect()

	return nil
}

// NewProtocol is called on all nodes of a Tree (except the root, since it is
// the one starting the protocol) so it's the Service that will be called to
// generate the PI on all others node.
// If you use CreateProtocolSDA, this will not be called, as the SDA will
// instantiate the protocol on its own. If you need more control at the
// instantiation of the protocol, use CreateProtocolService, and you can
// give some extra-configuration to your protocol in here.
func (s *Service) NewProtocol(tn *sda.TreeNodeInstance, conf *sda.GenericConfig) (sda.ProtocolInstance, error) {
	pi, err := prifi.NewPriFiSDAWrapperProtocol(tn)
	if err != nil {
		return nil, err
	}

	// Assert that pi has type PriFiSDAWrapper
	wrapper := pi.(*prifi.PriFiSDAWrapper)

	s.isPrifiRunning = true

	wrapper.SetConfig(&prifi.PriFiSDAWrapperConfig{
		Identities: s.identityMap,
		Role: s.role,
	})
	wrapper.Running = &s.isPrifiRunning

	return wrapper, nil
}

// save saves the actual identity
func (s *Service) save() {
	log.Lvl3("Saving service")
	b, err := network.MarshalRegisteredType(s.Storage)
	if err != nil {
		log.Error("Couldn't marshal service:", err)
	} else {
		err = ioutil.WriteFile(s.path+"/prifi.bin", b, 0660)
		if err != nil {
			log.Error("Couldn't save file:", err)
		}
	}
}

// tryLoad tries to load the configuration and updates if a configuration
// is found, else it returns an error.
func (s *Service) tryLoad() error {
	configFile := s.path + "/identity.bin"
	b, err := ioutil.ReadFile(configFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("Error while reading %s: %s", configFile, err)
	}
	if len(b) > 0 {
		_, msg, err := network.UnmarshalRegistered(b)
		if err != nil {
			return fmt.Errorf("Couldn't unmarshal: %s", err)
		}
		log.Lvl3("Successfully loaded")
		s.Storage = msg.(*Storage)
	}
	return nil
}

// newService receives the context and a path where it can write its
// configuration, if desired. As we don't know when the service will exit,
// we need to save the configuration on our own from time to time.
func newService(c *sda.Context, path string) sda.Service {
	log.Lvl4("Calling newService")
	s := &Service{
		ServiceProcessor: sda.NewServiceProcessor(c),
		path:             path,
		isPrifiRunning:   false,
	}

	c.RegisterProcessorFunc(network.TypeFromData(ConnectionRequest{}), s.HandleConnection)
	c.RegisterProcessorFunc(network.TypeFromData(DisconnectionRequest{}), s.HandleDisconnection)

	if err := s.tryLoad(); err != nil {
		log.Error(err)
	}
	return s
}

// parseDescription extracts a PriFiIdentity from a string
func parseDescription(description string) (*prifi.PriFiIdentity, error) {
	desc := strings.Split(description, " ")
	if len(desc) == 1 && desc[0] == "relay" {
		return &prifi.PriFiIdentity{
			Role: prifi.Relay,
			Id:   0,
		}, nil
	} else if len(desc) == 2 {
		id, err := strconv.Atoi(desc[1])
		if err != nil {
			return nil, errors.New("Unable to parse id:")
		} else {
			pid := prifi.PriFiIdentity{
				Id: id,
			}
			if desc[0] == "client" {
				pid.Role = prifi.Client
			} else if desc[0] == "trustee" {
				pid.Role = prifi.Trustee
			} else {
				return nil, errors.New("Invalid role.")
			}
			return &pid, nil
		}
	} else {
		return nil, errors.New("Invalid description.")
	}
}

// mapIdentities reads the group configuration to assign PriFi roles
// to server addresses and returns them with the server
// identity of the relay.
func mapIdentities(group *config.Group) (map[network.Address]prifi.PriFiIdentity, network.ServerIdentity) {
	m := make(map[network.Address]prifi.PriFiIdentity)
	var relay network.ServerIdentity

	// Read the description of the nodes in the config file to assign them PriFi roles.
	nodeList := group.Roster.List
	for i := 0; i < len(nodeList); i++ {
		si := nodeList[i]
		id, err := parseDescription(group.GetDescription(si))
		if err != nil {
			log.Info("Cannot parse node description, skipping:", err)
		} else {
			m[si.Address] = *id
			if id.Role == prifi.Relay {
				relay = *si
			}
		}
	}

	// Check that there is exactly one relay and at least one trustee and client
	t, c, r := 0, 0, 0

	for _, v := range m {
		switch v.Role {
		case prifi.Relay:
			r++
		case prifi.Client:
			c++
		case prifi.Trustee:
			t++
		}
	}

	if !(t > 0 && c > 0 && r == 1) {
		log.Fatal("Config file does not contain exactly one relay and at least one trustee and client.")
	}

	return m, relay
}

// readGroup reads the group description and sets up the Service struct fields
// accordingly. It *MUST* be called first when the node is started.
func (s *Service) readGroup(group *config.Group) {
	ids, relayId := mapIdentities(group)
	s.identityMap = ids
	s.relayIdentity = &relayId
	s.group = group
}
