package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ant0ine/go-json-rest"
	"github.com/nytlabs/streamtools/blocks"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"strings"
)

const (
	READ_MAX    = 1024768
	FILE_FORMAT = "0.1.0"
)

var (
	// channel that returns the next ID
	idChan chan string
)

// Daemon keeps track of all the blocks and connections
type Daemon struct {
	blockMap map[string]*blocks.Block
	Port     string
	Config   string
}

// The rootHandler returns information about the whole system
// TODO: create a generic static file handler
func (d *Daemon) rootHandler(w *rest.ResponseWriter, r *rest.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data, _ := Asset("gui/index.html")
	w.Write(data)
}

func (d *Daemon) cssHandler(w *rest.ResponseWriter, r *rest.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	data, _ := Asset("gui/static/main.css")
	w.Write(data)
}

func (d *Daemon) d3Handler(w *rest.ResponseWriter, r *rest.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	data, _ := Asset("gui/static/d3.v3.min.js")
	w.Write(data)
}

func (d *Daemon) mainjsHandler(w *rest.ResponseWriter, r *rest.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	data, _ := Asset("gui/static/main.js")
	w.Write(data)
}

func (d *Daemon) jqueryHandler(w *rest.ResponseWriter, r *rest.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	data, _ := Asset("gui/static/jquery-2.1.0.min.js")
	w.Write(data)
}

func (d *Daemon) underscoreHandler(w *rest.ResponseWriter, r *rest.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	data, _ := Asset("gui/static/underscore-min.js")
	w.Write(data)
}

// The createHandler creates new blocks
func (d *Daemon) createHandler(w *rest.ResponseWriter, r *rest.Request) {
	err := r.ParseForm()
	if err != nil {
		ApiResponse(w, 500, "BAD_REQUEST")
		return
	}

	var id string
	var blockType string
	fType, typeExists := r.Form["blockType"]
	fID, idExists := r.Form["id"]

	if typeExists == false {
		ApiResponse(w, 500, "MISSING_BLOCKTYPE")
		return
	} else {
		blockType = fType[0]
	}

	_, inLibrary := blocks.Library[blockType]
	if inLibrary == false {
		ApiResponse(w, 500, "INVALID_BLOCKTYPE")
		return
	}

	if idExists == false {
		id = d.getID()
	} else {

		if len(strings.TrimSpace(fID[0])) == 0 {
			ApiResponse(w, 500, "BAD_BLOCK_ID")
			return
		}

		_, notUnique := d.blockMap[fID[0]]
		if notUnique == true {
			ApiResponse(w, 500, "BLOCK_ID_ALREADY_EXISTS")
			return
		} else {
			id = fID[0]
		}
	}

	d.CreateBlock(blockType, id)

	ApiResponse(w, 200, "BLOCK_CREATED")
}

func (d *Daemon) deleteHandler(w *rest.ResponseWriter, r *rest.Request) {
	err := r.ParseForm()
	if err != nil {
		ApiResponse(w, 500, "BAD_REQUEST")
		return
	}

	_, hasID := r.Form["id"]
	if hasID == false {
		ApiResponse(w, 500, "MISSING_BLOCK_ID")
		return
	}

	id := r.Form["id"][0]

	err = d.DeleteBlock(id)

	if err != nil {
		ApiResponse(w, 500, err.Error())
		return
	}

	ApiResponse(w, 200, "BLOCK_DELETED")
}

func (d *Daemon) DeleteBlock(id string) error {
	block, ok := d.blockMap[id]
	if ok == false {
		return errors.New("BLOCK_NOT_FOUND")
	}

	// delete inbound channels
	for k, _ := range block.InBlocks {
		d.blockMap[k].AddChan <- &blocks.OutChanMsg{
			Action: blocks.DELETE_OUT_CHAN,
			ID:     block.ID,
		}

		log.Println("disconnecting \"" + block.ID + "\" from \"" + k + "\"")

		delete(d.blockMap[k].OutBlocks, block.ID)

		if d.blockMap[k].BlockType == "connection" {
			d.DeleteBlock(k)
		}
	}

	// delete outbound channels
	for k, _ := range block.OutBlocks {
		delete(d.blockMap[k].InBlocks, block.ID)
		if d.blockMap[k].BlockType == "connection" {
			d.DeleteBlock(k)
		}
	}

	// delete the block itself
	block.QuitChan <- true
	delete(d.blockMap, id)

	return nil
}

// The connectHandler connects together two blocks
func (d *Daemon) connectHandler(w *rest.ResponseWriter, r *rest.Request) {
	var id string

	err := r.ParseForm()
	if err != nil {
		ApiResponse(w, 500, "BAD_REQUEST")
		return
	}

	_, hasFrom := r.Form["from"]
	if hasFrom == false {
		ApiResponse(w, 500, "MISSING_FROM_BLOCK_ID")
		return
	}

	_, hasTo := r.Form["to"]
	if hasTo == false {
		ApiResponse(w, 500, "MISSING_TO_BLOCK_ID")
		return
	}

	fID, hasID := r.Form["id"]
	if hasID == false {
		id = d.getID()
	} else {

		if len(strings.TrimSpace(fID[0])) == 0 {
			ApiResponse(w, 500, "BAD_CONNECTION_ID")
			return
		}

		_, ok := d.blockMap[fID[0]]
		if ok == false {
			id = fID[0]
		} else {
			ApiResponse(w, 500, "BLOCK_ID_ALREADY_EXISTS")
			return
		}
	}

	from := r.Form["from"][0]
	to := r.Form["to"][0]

	if len(from) == 0 {
		ApiResponse(w, 500, "MISSING_FROM_BLOCK_ID")
		return
	}

	if len(to) == 0 {
		ApiResponse(w, 500, "MISSING_TO_BLOCK_ID")
		return
	}

	_, exists := d.blockMap[from]
	if exists == false {
		ApiResponse(w, 500, "FROM_BLOCK_NOT_FOUND")
		return
	}

	_, exists = d.blockMap[strings.Split(to, "/")[0]]
	if exists == false {
		ApiResponse(w, 500, "TO_BLOCK_NOT_FOUND")
		return
	}

	err = d.CreateConnection(from, to, id)
	if err != nil {
		ApiResponse(w, 500, "TO_ROUTE_NOT_FOUND")
		return
	}

	ApiResponse(w, 200, "CONNECTION_CREATED")
}

// this should probably be async someday
func (d *Daemon) routeMsg(id string, route string, outMsg interface{}) interface{} {
	ResponseChan := make(chan interface{})
	blockRouteChan := d.blockMap[id].Routes[route]
	blockRouteChan <- &blocks.BMsg{
		Msg:          outMsg,
		ResponseChan: ResponseChan,
	}
	respMsg := <-ResponseChan
	return respMsg
}

func (d *Daemon) getID() string {
	id := <-idChan
	_, ok := d.blockMap[id]
	for ok {
		id = <-idChan
		_, ok = d.blockMap[id]
	}
	return id
}

// The routeHandler deals with any incoming message sent to an arbitrary block endpoint
func (d *Daemon) routeHandler(w *rest.ResponseWriter, r *rest.Request) {
	//id := strings.Split(r.URL.Path, "/")[2]
	//route := strings.Split(r.URL.Path, "/")[3]

	id, ok := r.PathParams["id"]
	if ok == false {
		ApiResponse(w, 500, "MISSING_BLOCK_ID")
		return
	}

	route, ok := r.PathParams["route"]
	if ok == false {
		ApiResponse(w, 500, "MISSING_ROUTE")
		return
	}

	_, ok = d.blockMap[id]
	if ok == false {
		ApiResponse(w, 500, "BLOCK_ID_NOT_FOUND")
		return
	}

	_, ok = d.blockMap[id].Routes[route]
	if ok == false {
		ApiResponse(w, 500, "ROUTE_NOT_FOUND")
		return
	}

	msg, err := ioutil.ReadAll(io.LimitReader(r.Body, READ_MAX))

	if err != nil {
		ApiResponse(w, 500, "BAD_REQUEST")
		return
	}

	var outMsg interface{}

	if len(msg) > 0 {
		err = json.Unmarshal(msg, &outMsg)
		if err != nil {
			log.Println(msg)
			ApiResponse(w, 500, "BAD_JSON")
			return
		}
	}

	respJson, err := json.Marshal(d.routeMsg(id, route, outMsg))
	if err != nil {
		ApiResponse(w, 500, "BAD_RESPONSE_FROM_BLOCK")
		return
	}

	DataResponse(w, respJson)
}

func (d *Daemon) libraryHandler(w *rest.ResponseWriter, r *rest.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(blocks.LibraryBlob)))
	fmt.Fprint(w, blocks.LibraryBlob)
}

func (d *Daemon) listBlocks() []map[string]interface{} {
	blockList := []map[string]interface{}{}
	for _, v := range d.blockMap {
		blockItem := make(map[string]interface{})
		blockItem["BlockType"] = v.BlockType
		blockItem["ID"] = v.ID
		blockItem["InBlocks"] = []string{}
		blockItem["OutBlocks"] = []string{}
		blockItem["Routes"] = []string{}
		for k, _ := range v.InBlocks {
			blockItem["InBlocks"] = append(blockItem["InBlocks"].([]string), k)
		}
		for k, _ := range v.OutBlocks {
			blockItem["OutBlocks"] = append(blockItem["OutBlocks"].([]string), k)
		}
		for k, _ := range v.Routes {
			blockItem["Routes"] = append(blockItem["Routes"].([]string), k)
		}
		blockList = append(blockList, blockItem)
	}
	return blockList
}

func (d *Daemon) makeExport() map[string]interface{} {
	blockList := d.listBlocks()
	blocks := []map[string]interface{}{}
	conns := []map[string]interface{}{}

	for _, b := range blockList {
		_, ok := d.blockMap[b["ID"].(string)]
		if !ok {
			continue
		}

		delete(b, "Routes")

		_, ok = d.blockMap[b["ID"].(string)].Routes["get_rule"]
		if ok {
			var outMsg interface{}
			b["rule"] = d.routeMsg(b["ID"].(string), "get_rule", outMsg)
		} else {
			if len(b["InBlocks"].([]string)) == 1 && len(b["OutBlocks"].([]string)) == 1 {
				b["from"] = b["InBlocks"].([]string)[0]

				route := d.blockMap[b["ID"].(string)].OutBlocks[b["OutBlocks"].([]string)[0]]
				if len(route) > 0 {
					b["to"] = b["OutBlocks"].([]string)[0]
					b["route"] = route
				} else {
					b["to"] = b["OutBlocks"].([]string)[0]
				}
			}
		}

		b["id"] = b["ID"]
		b["type"] = b["BlockType"]

		delete(b, "InBlocks")
		delete(b, "OutBlocks")
		delete(b, "BlockType")
		delete(b, "ID")

		if b["type"] != "connection" {
			blocks = append(blocks, b)
		} else {
			conns = append(conns, b)
		}
	}

	exportObj := map[string]interface{}{
		"blocks":      blocks,
		"connections": conns,
		"version":     FILE_FORMAT,
	}
	return exportObj
}

func (d *Daemon) exportHandler(w *rest.ResponseWriter, r *rest.Request) {
	blob, _ := json.Marshal(d.makeExport())

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
	fmt.Fprint(w, string(blob))
}

func (d *Daemon) importHandler(w *rest.ResponseWriter, r *rest.Request) {
	msg, err := ioutil.ReadAll(io.LimitReader(r.Body, READ_MAX))

	if err != nil {
		ApiResponse(w, 500, "BAD_REQUEST")
		return
	}

	var outMsg interface{}

	if len(msg) > 0 {
		err = json.Unmarshal(msg, &outMsg)
		if err != nil {
			log.Println(msg)
			ApiResponse(w, 500, "BAD_JSON")
			return
		}
	}

	err = d.importConfig(outMsg)

	if err != nil {
		ApiResponse(w, 500, "IMPORT_FAIL")
	}

	ApiResponse(w, 500, "IMPORT_SUCCESS")
}

func (d *Daemon) importFile(filename string) error {
	var config interface{}

	b, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Println("could not import " + filename)
		return err
	}

	err = json.Unmarshal(b, &config)
	if err != nil {
		log.Println("could not unmarshal " + filename)
		return err
	}

	err = d.importConfig(config)

	if err != nil {
		log.Println("could not import")
		return err
	}
	return nil
}

func (d *Daemon) importConfig(config interface{}) error {
	collisionMap := make(map[string]string)

	cm, ok := config.(map[string]interface{})
	if !ok {
		return errors.New("bad config format")
	}

	blocks := cm["blocks"].([]interface{})
	connections := cm["connections"].([]interface{})
	for _, block := range blocks {
		b := block.(map[string]interface{})
		_, ok := d.blockMap[b["id"].(string)]
		if ok {
			var newID string
			unique := false
			count := 1
			for unique == false {
				newID = b["id"].(string) + "_" + fmt.Sprintf("%d", count)
				_, ok := d.blockMap[newID]
				if !ok {
					unique = true
				}
				count++
			}
			collisionMap[b["id"].(string)] = newID
		} else {
			collisionMap[b["id"].(string)] = b["id"].(string)
		}

		d.CreateBlock(b["type"].(string), collisionMap[b["id"].(string)])
		_, ok = b["rule"]
		if ok {
			d.routeMsg(collisionMap[b["id"].(string)], "set_rule", b["rule"])
		}
	}

	for _, conn := range connections {
		c := conn.(map[string]interface{})
		_, ok := d.blockMap[c["id"].(string)]
		if ok {
			var newID string
			unique := false
			count := 1
			for unique == false {
				newID = c["id"].(string) + "_" + fmt.Sprintf("%d", count)
				_, ok := d.blockMap[newID]
				if !ok {
					unique = true
				}
				count++
			}
			collisionMap[c["id"].(string)] = newID
		} else {
			collisionMap[c["id"].(string)] = c["id"].(string)
		}

		toBlock := collisionMap[c["to"].(string)]
		_, ok = c["route"]
		if ok {
			toBlock = toBlock + "/" + c["route"].(string)
		}
		fromBlock := collisionMap[c["from"].(string)]
		blockID := collisionMap[c["id"].(string)]

		d.CreateConnection(fromBlock, toBlock, blockID)
	}

	return nil
}

func (d *Daemon) listHandler(w *rest.ResponseWriter, r *rest.Request) {
	blob, _ := json.Marshal(d.listBlocks())

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
	fmt.Fprint(w, string(blob))
}

func (d *Daemon) CreateConnection(from string, to string, ID string) error {
	d.CreateBlock("connection", ID)

	d.blockMap[from].AddChan <- &blocks.OutChanMsg{
		Action:  blocks.CREATE_OUT_CHAN,
		OutChan: d.blockMap[ID].InChan,
		ID:      ID,
	}

	toParts := strings.Split(to, "/")
	log.Println(to, toParts)
	var route string
	if len(toParts) == 2 {
		route = toParts[1]
	}

	switch len(toParts) {
	case 1:
		d.blockMap[ID].AddChan <- &blocks.OutChanMsg{
			Action:  blocks.CREATE_OUT_CHAN,
			OutChan: d.blockMap[to].InChan,
			ID:      to,
		}
	case 2:
		d.blockMap[ID].AddChan <- &blocks.OutChanMsg{
			Action:  blocks.CREATE_OUT_CHAN,
			OutChan: d.blockMap[toParts[0]].Routes[route],
			ID:      to,
		}
	default:
		err := errors.New("malformed to route specification")
		return err
	}

	// add the from block to the list of inblocks for connection.
	d.blockMap[from].OutBlocks[ID] = ""
	d.blockMap[ID].InBlocks[from] = ""
	d.blockMap[ID].OutBlocks[toParts[0]] = route
	d.blockMap[toParts[0]].InBlocks[ID] = route

	log.Println("connected", d.blockMap[from].ID, "to", d.blockMap[toParts[0]].ID)

	return nil
}

func (d *Daemon) CreateBlock(name string, ID string) {
	// TODO: Clean this up.
	//
	// In order to avoid data races the blocks held in daemon's blockMap
	// are not the same blocks held in each block routine. When CreateBlock
	// is called, we actually create two blocks: one to store in daemon's
	// blockMap and one to send to the block routine.
	//
	// The block stored in daemon's blockmap doesn't make use of OutChans as
	// a block's OutChans can be dynamically modified when connections are
	// added or deleted. All of the other fields, such as ID, name, and all
	// the channels that go into the block (inChan, Routes) are the SAME
	// in both the daemon blockMap block and the blockroutine block.
	//
	// Becauase of this very minor difference it would be a huge semantic help
	// if the type going to the blockroutines was actually different than the
	// type being kept in daemon's blockmap.
	//
	// Modifications to blocks in daemon's blockMap will obviously not
	// proliferate to blockroutines and all changes (such as adding outchans)
	// can only be done through messages. A future daemon block type might
	// want to restrict how daemon blocks can be used, such as creating
	// getters and no setters. Or perhaps a setter automatically takes care
	// of sending a message to the blockroutine to emulate the manipulation
	// of a single variable.

	// create the block that will be stored in blockMap
	b, _ := blocks.NewBlock(name, ID)
	//d.createRoutes(b)
	d.blockMap[b.ID] = b
	d.blockMap[b.ID].InBlocks = make(map[string]string)
	d.blockMap[b.ID].OutBlocks = make(map[string]string)

	// create the block that will be sent to the blockroutine and copy all
	// chan references from the previously created block
	c, _ := blocks.NewBlock(name, ID)
	for k, v := range b.Routes {
		c.Routes[k] = v
	}

	c.InChan = b.InChan
	c.AddChan = b.AddChan
	c.QuitChan = b.QuitChan

	//create outchans for use only by blockroutine block.
	c.OutChans = make(map[string]chan *blocks.BMsg)

	go blocks.Library[name].Routine(c)

	log.Println("started block \"" + ID + "\" of type " + name)
}

func (d *Daemon) Run() {

	// start the ID Service
	idChan = make(chan string)
	go IDService(idChan)

	// start the library service
	blocks.BuildLibrary()

	// initialise the block maps
	d.blockMap = make(map[string]*blocks.Block)

	handler := rest.ResourceHandler{
		EnableRelaxedContentType: true,
	}

	if len(d.Config) > 0 {
		err := d.importFile(d.Config)
		if err != nil {
			log.Println("could not import config file")
		}
	}

	//TODO: make this a _real_ restful API
	handler.SetRoutes(
		rest.Route{"GET", "/", d.rootHandler},
		rest.Route{"GET", "/static/main.css", d.cssHandler},
		rest.Route{"GET", "/static/d3.v3.min.js", d.d3Handler},
		rest.Route{"GET", "/static/main.js", d.mainjsHandler},
		rest.Route{"GET", "/static/jquery-2.1.0.min.js", d.jqueryHandler},
		rest.Route{"GET", "/static/underscore-min.js", d.underscoreHandler},
		rest.Route{"GET", "/library", d.libraryHandler},
		rest.Route{"GET", "/list", d.listHandler},
		rest.Route{"GET", "/create", d.createHandler},
		rest.Route{"GET", "/delete", d.deleteHandler},
		rest.Route{"GET", "/connect", d.connectHandler},
		rest.Route{"GET", "/blocks/:id/:route", d.routeHandler},
		rest.Route{"POST", "/blocks/:id/:route", d.routeHandler},
		rest.Route{"GET", "/export", d.exportHandler},
		rest.Route{"POST", "/import", d.importHandler},
	)
	log.Println("starting stream tools on port", d.Port)
	err := http.ListenAndServe(":"+d.Port, &handler)
	if err != nil {
		log.Fatalf(err.Error())
	}
}
