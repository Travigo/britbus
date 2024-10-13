package naptan

import (
	"fmt"
	"time"

	"github.com/travigo/travigo/pkg/ctdf"
)

type StopArea struct {
	CreationDateTime     string `xml:",attr"`
	ModificationDateTime string `xml:",attr"`
	Status               string `xml:",attr"`

	StopAreaCode          string
	Name                  string
	AdministrativeAreaRef string
	StopAreaType          string

	Location *Location

	Stops []StopPoint
}

func (orig *StopArea) ToCTDF() *ctdf.StopGroup {
	creationTime, _ := time.Parse(DateTimeFormat, orig.CreationDateTime)
	modificationTime, _ := time.Parse(DateTimeFormat, orig.ModificationDateTime)

	ctdfStopGroup := ctdf.StopGroup{
		PrimaryIdentifier: fmt.Sprintf(ctdf.StopGroupIDFormat, orig.StopAreaCode),
		OtherIdentifiers:  []string{fmt.Sprintf("GB:ATCO:%s", orig.StopAreaCode)},

		Name:                 orig.Name,
		Status:               orig.Status,
		CreationDateTime:     creationTime,
		ModificationDateTime: modificationTime,
	}

	switch orig.StopAreaType {
	case "GPBS":
		ctdfStopGroup.Type = "pair"
	case "GCLS":
		ctdfStopGroup.Type = "cluster"
	case "GCCH":
		ctdfStopGroup.Type = "cluster"
	case "GBCS":
		ctdfStopGroup.Type = "bus_station"
	case "GMLT":
		ctdfStopGroup.Type = "multimode_interchange"
	case "GTMU", "GRLS":
		ctdfStopGroup.Type = "station"
	case "GFTD":
		ctdfStopGroup.Type = "port"
	default:
		ctdfStopGroup.Type = "unknown"
	}

	return &ctdfStopGroup
}
