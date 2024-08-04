package clustermanager

import "fmt"

// FabricOSDInfo contains information about an OSD that is attached to a node
type FabricOSDInfo struct {
	id       string
	address  string
	port     string
	subnqn   string
	hostname string
}

type FabricMap struct {
	osdsByDomin map[string]map[string][]FabricOSDInfo
}

func NewOSDNodeMap() FabricMap {
	return FabricMap{
		osdsByDomin: make(map[string]map[string][]FabricOSDInfo),
	}
}

// AddOSD adds an OSD to the map
func (o FabricMap) AddOSD(osdID, domainName, node, address, port, subnqn string) {
	if _, exists := o.osdsByDomin[domainName]; !exists {
		o.osdsByDomin[domainName] = make(map[string][]FabricOSDInfo)
	}
	o.osdsByDomin[domainName][node] = append(o.osdsByDomin[domainName][node], FabricOSDInfo{
		id:       osdID,
		address:  address,
		port:     port,
		subnqn:   subnqn,
		hostname: node,
	})
}

// RemoveOSD removes an OSD from the map
func (o *FabricMap) RemoveOSD(osdID, domainName, node string) {
	osds := o.osdsByDomin[domainName][node]
	for i, osdInfo := range osds {
		if osdInfo.id == osdID {
			o.osdsByDomin[domainName][node] = append(osds[:i], osds[i+1:]...)
			if len(o.osdsByDomin[domainName][node]) == 0 {
				delete(o.osdsByDomin[domainName], node)
			}
			break
		}
	}
}

// FindNodeByOSD finds the node that an OSD is attached to
func (o *FabricMap) FindNodeByOSD(osdID, domainName string) (string, error) {
	for node, osds := range o.osdsByDomin[domainName] {
		for _, osdInfo := range osds {
			if osdInfo.id == osdID {
				return node, nil
			}
		}
	}
	return "", fmt.Errorf("OSD %s is not attached to any node", osdID)
}

// FindOSDBySubNQN finds the OSDInfo that has a given subnqn
func (o *FabricMap) FindOSDBySubNQN(subnqn string) (FabricOSDInfo, error) {
	for _, osdsByNode := range o.osdsByDomin {
		for _, osds := range osdsByNode {
			for _, osdInfo := range osds {
				if osdInfo.subnqn == subnqn {
					return osdInfo, nil
				}
			}
		}
	}
	return FabricOSDInfo{}, fmt.Errorf("OSD not found for subnqn %s", subnqn)
}

// FindOSDsByNode returns the OSDs that are attached to a node
func (o *FabricMap) FindOSDsByNode(domainName, node string) ([]FabricOSDInfo, bool) {
	osds, ok := o.osdsByDomin[domainName][node]
	return osds, ok
}

// GetNodes returns the attachable nodes
func (o *FabricMap) GetNodes(domainName string) []string {
	nodes := make([]string, 0, len(o.osdsByDomin[domainName]))
	for node := range o.osdsByDomin[domainName] {
		nodes = append(nodes, node)
	}
	return nodes
}
