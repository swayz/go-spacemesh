package mesh

import (
	"bytes"
	"container/list"
	"errors"
	"fmt"
	"github.com/spacemeshos/go-spacemesh/database"
	"github.com/spacemeshos/go-spacemesh/log"
	"sync"
)

type layerMutex struct {
	m            sync.Mutex
	layerWorkers uint32
}

type MeshDB struct {
	log.Log
	layers             database.DB
	blocks             database.DB
	contextualValidity database.DB //map blockId to contextualValidation state of block
	orphanBlocks       map[LayerID]map[BlockID]struct{}
	orphanBlockCount   int32
	layerMutex         map[LayerID]*layerMutex
	lhMutex            sync.Mutex
}

func NewPersistentMeshDB(path string, log log.Log) *MeshDB {
	bdb := database.NewLevelDbStore(path+"blocks", nil, nil)
	ldb := database.NewLevelDbStore(path+"layers", nil, nil)
	vdb := database.NewLevelDbStore(path+"validity", nil, nil)
	ll := &MeshDB{
		Log:                log,
		blocks:             bdb,
		layers:             ldb,
		contextualValidity: vdb,
		orphanBlocks:       make(map[LayerID]map[BlockID]struct{}),
		layerMutex:         make(map[LayerID]*layerMutex),
	}
	return ll
}

func NewMemMeshDB(log log.Log) *MeshDB {
	db := database.NewMemDatabase()
	ll := &MeshDB{
		Log:                log,
		blocks:             db,
		layers:             db,
		contextualValidity: db,
		orphanBlocks:       make(map[LayerID]map[BlockID]struct{}),
		layerMutex:         make(map[LayerID]*layerMutex),
	}
	return ll
}

func (m *MeshDB) Close() {
	m.blocks.Close()
	m.layers.Close()
	m.contextualValidity.Close()
}

func (m *MeshDB) getLayer(index LayerID) (*Layer, error) {
	ids, err := m.layers.Get(index.ToBytes())
	if err != nil {
		return nil, fmt.Errorf("error getting layer %v from database ", index)
	}

	l := NewLayer(LayerID(index))
	if len(ids) == 0 {
		return nil, fmt.Errorf("no ids for layer %v in database ", index)
	}

	blockIds, err := bytesToBlockIds(ids)
	if err != nil {
		return nil, errors.New("could not get all blocks from database ")
	}

	blocks, err := m.getLayerBlocks(blockIds)
	if err != nil {
		return nil, errors.New("could not get all blocks from database " + err.Error())
	}

	l.SetBlocks(blocks)

	return l, nil
}

// addBlock adds a new block to block DB and updates the correct layer with the new block
// if this is the first occurence of the layer a new layer object will be inserted into layerDB as well
func (m *MeshDB) addBlock(block *Block) error {
	_, err := m.blocks.Get(block.ID().ToBytes())
	if err == nil {
		log.Debug("block ", block.ID(), " already exists in database")
		return errors.New("block " + string(block.ID()) + " already exists in database")
	}
	err = m.writeBlock(block)
	if err != nil {
		return err
	}
	return nil
}

func (m *MeshDB) getBlock(id BlockID) (*Block, error) {
	b, err := m.blocks.Get(id.ToBytes())
	if err != nil {
		return nil, errors.New("could not find block in database")
	}
	blk, err := BytesAsBlock(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return &blk, nil
}

func (m *MeshDB) getContextualValidity(id BlockID) (bool, error) {
	b, err := m.contextualValidity.Get(id.ToBytes())
	return b[0] == 1, err //bytes to bool
}

func (m *MeshDB) setContextualValidity(id BlockID, valid bool) error {
	//todo implement
	//todo concurrency
	var v []byte
	if valid {
		v = TRUE
	}
	m.contextualValidity.Put(id.ToBytes(), v)
	return nil
}

func (m *MeshDB) writeBlock(bl *Block) error {
	bytes, err := BlockAsBytes(*bl)
	if err != nil {
		return fmt.Errorf("could not encode bl")

	}
	if b, err := m.blocks.Get(bl.ID().ToBytes()); err == nil && b != nil {
		return fmt.Errorf("bl %v already in database ", bl.ID())
	}

	if err := m.blocks.Put(bl.ID().ToBytes(), bytes); err != nil {
		return fmt.Errorf("could not add bl to %v databacse %v", bl.ID(), err)
	}

	m.updateLayerWithBlock(bl)
	return nil
}

//todo this overwrites the previous value if it exists
func (m *MeshDB) addLayer(layer *Layer) error {
	if len(layer.blocks) == 0 {
		m.layers.Put(layer.Index().ToBytes(), []byte{})
		return nil
	}

	//add blocks to mDB
	for _, bl := range layer.blocks {
		m.writeBlock(bl)
	}

	return nil
}

func (m *MeshDB) updateLayerWithBlock(block *Block) error {
	lm := m.getLayerMutex(block.LayerIndex)
	defer m.endLayerWorker(block.LayerIndex)
	lm.m.Lock()
	defer lm.m.Unlock()
	ids, err := m.layers.Get(block.LayerIndex.ToBytes())
	var blockIds map[BlockID]bool
	if err != nil {
		//layer doesnt exist, need to insert new layer
		ids = []byte{}
		blockIds = make(map[BlockID]bool)
	} else {
		blockIds, err = bytesToBlockIds(ids)
		if err != nil {
			return errors.New("could not get all blocks from database ")
		}
	}
	m.Info("added block %v to layer %v", block.ID(), block.LayerIndex)
	blockIds[block.ID()] = true
	w, err := blockIdsAsBytes(blockIds)
	if err != nil {
		return errors.New("could not encode layer block ids")
	}
	m.layers.Put(block.LayerIndex.ToBytes(), w)
	return nil
}

func (m *MeshDB) getLayerBlocks(ids map[BlockID]bool) ([]*Block, error) {

	blocks := make([]*Block, 0, len(ids))
	for k := range ids {
		block, err := m.getBlock(k)
		if err != nil {
			return nil, errors.New("could not retrieve block " + fmt.Sprint(k) + " " + err.Error())
		}
		blocks = append(blocks, block)
	}

	return blocks, nil
}

//try delete layer Handler (deletes if pending pendingCount is 0)
func (m *MeshDB) endLayerWorker(index LayerID) {
	m.lhMutex.Lock()
	defer m.lhMutex.Unlock()

	ll, found := m.layerMutex[index]
	if !found {
		panic("trying to double close layer mutex")
	}

	ll.layerWorkers--
	if ll.layerWorkers == 0 {
		delete(m.layerMutex, index)
	}
}

//returns the existing layer Handler (crates one if doesn't exist)
func (m *MeshDB) getLayerMutex(index LayerID) *layerMutex {
	m.lhMutex.Lock()
	defer m.lhMutex.Unlock()
	ll, found := m.layerMutex[index]
	if !found {
		ll = &layerMutex{}
		m.layerMutex[index] = ll
	}
	ll.layerWorkers++
	return ll
}

type MeshCache struct {
	*MeshDB
}

func (mc MeshCache) Get(id BlockID) (*Block, error) {
	b, err := mc.getBlock(id)
	if b == nil && err == nil {
		err = errors.New("could not find block in database")
	}
	return b, err
}

func (mc MeshCache) Put(b *Block) error {
	return mc.addBlock(b)
}

func (mc MeshCache) PutLayer(l *Layer) error {
	return mc.addLayer(l)
}

func (mc MeshCache) GetLayer(l LayerID) (*Layer, error) {
	return mc.getLayer(l)
}

func (mc MeshCache) ForBlockInView(view map[BlockID]struct{}, layer LayerID, foo func(block *Block), errHandler func(err error)) {
	stack := list.New()
	for b := range view {
		stack.PushFront(b)
	}
	set := make(map[BlockID]struct{})
	for b := stack.Front(); b != nil; b = stack.Front() {
		a := stack.Remove(stack.Front()).(BlockID)
		block, err := mc.Get(a)
		if err != nil {
			errHandler(err)
		}
		foo(block)
		//push children to bfs queue
		for _, id := range block.ViewEdges {
			bChild, err := mc.Get(id)
			if err != nil {
				errHandler(err)
			}
			if bChild.Layer() >= layer { //dont traverse too deep
				if _, found := set[bChild.ID()]; !found {
					set[bChild.ID()] = struct{}{}
					stack.PushBack(bChild.ID())
				}
			}
		}
	}
	return
}
