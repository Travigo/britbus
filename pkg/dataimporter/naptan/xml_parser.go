package naptan

import (
	"encoding/xml"
	"io"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/html/charset"
)

func ParseXMLFile(reader io.Reader) (*NaPTAN, error) {
	naptan := NaPTAN{}
	naptan.StopPoints = []*StopPoint{}
	naptan.StopAreas = []*StopArea{}

	d := xml.NewDecoder(reader)
	d.CharsetReader = charset.NewReaderLabel
	for {
		tok, err := d.Token()
		if tok == nil || err == io.EOF {
			// EOF means we're done.
			break
		} else if err != nil {
			log.Fatal().Msgf("Error decoding token: %s", err)
			return nil, err
		}

		switch ty := tok.(type) {
		case xml.StartElement:
			if ty.Name.Local == "NaPTAN" {
				for i := 0; i < len(ty.Attr); i++ {
					attr := ty.Attr[i]

					switch attr.Name.Local {
					case "CreationDateTime":
						naptan.CreationDateTime = attr.Value
					case "ModificationDateTime":
						naptan.ModificationDateTime = attr.Value
					case "SchemaVersion":
						naptan.SchemaVersion = attr.Value
					}
				}

				validate := naptan.Validate()
				if validate != nil {
					return nil, validate
				}
			} else if ty.Name.Local == "StopPoint" {
				var stopPoint StopPoint

				if err = d.DecodeElement(&stopPoint, &ty); err != nil {
					log.Fatal().Msgf("Error decoding item: %s", err)
				} else {
					stopPoint.Location.UpdateCoordinates()
					naptan.StopPoints = append(naptan.StopPoints, &stopPoint)
				}
			} else if ty.Name.Local == "StopArea" {
				var stopArea StopArea

				if err = d.DecodeElement(&stopArea, &ty); err != nil {
					log.Fatal().Msgf("Error decoding item: %s", err)
				} else {
					stopArea.Location.UpdateCoordinates()
					naptan.StopAreas = append(naptan.StopAreas, &stopArea)
				}
			}
		default:
		}
	}

	log.Info().Msgf("Successfully parsed document")
	log.Info().Msgf(" - Last modified %s", naptan.ModificationDateTime)
	log.Info().Msgf(" - Contains %d stops", len(naptan.StopPoints))
	log.Info().Msgf(" - Contains %d stop areas", len(naptan.StopAreas))

	return &naptan, nil
}
