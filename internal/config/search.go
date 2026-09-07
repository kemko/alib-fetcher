package config

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/text/encoding/charmap"
)

// SearchConfig describes the Alib search sources for one recipient.
type SearchConfig struct {
	Categories []string
	Filters    map[string][]string
	Queries    []map[string]string
}

const (
	searchFieldAuthor      = "author"
	searchFieldTitle       = "title"
	searchFieldSeria       = "seria"
	searchFieldPublisher   = "izdat"
	searchFieldCityPub     = "gorodiz"
	searchFieldISBN        = "isbnp"
	searchFieldYearFrom    = "god1"
	searchFieldYearTo      = "god2"
	searchFieldPriceFrom   = "cena1"
	searchFieldPriceTo     = "cena2"
	searchFieldAnnotation  = "sod"
	searchFieldSeller      = "bsonly"
	searchFieldCitySeller  = "gorod"
	searchFieldLastDays    = "lday"
	searchFieldNoReprint   = "noreprint"
	searchFieldNoEngraving = "nograv"
	searchFieldPhotoOnly   = "fotoonly"
	searchFieldMinus       = "minus"
	searchFieldPriceRange  = "sumfind"
	searchFieldCategory    = "tipfind"
	searchFieldSort        = "sortby"
)

var alibSearchFields = []string{
	searchFieldAuthor,
	searchFieldTitle,
	searchFieldSeria,
	searchFieldPublisher,
	searchFieldCityPub,
	searchFieldISBN,
	searchFieldYearFrom,
	searchFieldYearTo,
	searchFieldPriceFrom,
	searchFieldPriceTo,
	searchFieldAnnotation,
	searchFieldSeller,
	searchFieldCitySeller,
	searchFieldLastDays,
	searchFieldNoReprint,
	searchFieldNoEngraving,
	searchFieldPhotoOnly,
	searchFieldMinus,
	searchFieldPriceRange,
	searchFieldCategory,
	searchFieldSort,
}

var alibTipfindValues = makeSet(strings.Fields(`
	prod prog t18 t19poem t2mv t46 t50 t5rekom tacademia tagro tamed tanart tandet tandokum tandr tangeo tangl
	tanist tanlit tanotkr tanpaper tanper tanpoem tanprom tanproz tanvoina tarheolog tarhitek tarktika tastro tauto
	tavstralia tbalkan tbazia tbiograf tbiolog tbona tbrail tbrit tbros tbuddizm tcentru tchina tdelo tdetekti tdeto
	tdetskaz tdetton tdetuch tdetz tdmed tdo10 tdo15 tdo1m tdo1s tdo1t tdo20 tdo2s tdo30 tdo3s tdo50 tdo5s
	tdo5t tdo70 tdo7s tdokum tdrafr tdrama tdrazia tdreu tdris tdrkult tdrnauka tdrrelig tdrteh tegipet
	tekolog tekonom tenergo tetnos tfant tfinans tfiz tfmed tfoto tgeo tger thimia thmed thobby thorror thrist
	tind tiroman tiross0 tiross1 tiross2 tiross3 tirossdr tisant tisbur tisdrev tiskus tislam tisnov tisperv
	tissred tistor tiudaica tiudaizm tjivo tjp tjurnal tkav tkiber tkino tkmed tknigove tkoll tkomiks tkrym
	tkulinar tlegprom tletu tlingbook tlingdr tlingrus tlingslov tlingsssr tlinguch tlit tlroman tmagia tmaps tmarx
	tmat tmed tmemart tmemlit tmemnauka tmemsport tmemvoina tmetall tmini tmoda tmsk tmuzei tnauka tnedra
	tnmed tnobel tnotes to19proz tofant togorod tohota toproz totdel totkr tpaper tpartner tpedagog tper tphant
	tphbur tphil tphonov tphsred tphvost tphznov tpmed tpodarok tpodp tpoem tpolit tposob10 tposobsam tposobu
	tprikl tprom tpsi tput tr1917 tradio tramka transavia transgd transnaz transvod trazved trelig tremont
	treprint trom tross trudno tsamer tsazia tsbp tsbvl tsdelay tsever tsex tshah tsib tsjzl tskan tskidka
	tslovar tslp tsocio tspb tsport tsppv tsprav tstroit tstroy tuamer tuga tukr tumor tural tvdelo tvet
	tvlit tvoina tz18proz tz19proz tzakon tzfant tzproz tzross tzveri`))

type searchErrorContext struct {
	fields map[string]string
}

// BuildSearchURLs builds category, independent-filter, and compound Alib URLs.
func BuildSearchURLs(categories []string, filters map[string][]string, queries []map[string]string) ([]string, error) {
	return buildSearchURLs(categories, filters, queries, searchErrorContext{fields: map[string]string{
		"categories": "categories",
	}})
}

// BuildAlibURLs builds the URLs described by search.
func BuildAlibURLs(search SearchConfig) ([]string, error) {
	return BuildSearchURLs(search.Categories, search.Filters, search.Queries)
}

func buildSearchURLs(
	categories []string,
	filters map[string][]string,
	queries []map[string]string,
	context searchErrorContext,
) ([]string, error) {
	if len(categories) == 0 && len(filters) == 0 && len(queries) == 0 {
		return nil, fmt.Errorf(
			"%w: categories, filters, and queries must not all be empty",
			ErrInvalid,
		)
	}

	if err := validateCategories(categories, context); err != nil {
		return nil, err
	}
	if err := validateFilters(filters, context); err != nil {
		return nil, err
	}
	if err := validateQueries(queries, context); err != nil {
		return nil, err
	}

	return buildSearchEndpoints(categories, filters, queries, context)
}

func validateCategories(categories []string, context searchErrorContext) error {
	for index, category := range categories {
		if !isASCIIWord(category) {
			return fmt.Errorf(
				"%w: %s item %d contains invalid category %q",
				ErrInvalid,
				context.field("categories"),
				index,
				category,
			)
		}
	}
	return nil
}

func validateFilters(filters map[string][]string, context searchErrorContext) error {
	if err := validateFilterKeys(filters, context); err != nil {
		return err
	}
	for _, field := range alibSearchFields {
		values, present := filters[field]
		if present && len(values) == 0 {
			return invalidSearchValue(context, field, "must contain at least one value")
		}
		for index, value := range values {
			if err := validateSearchValue(field, value, context, index); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateQueries(queries []map[string]string, context searchErrorContext) error {
	for index, query := range queries {
		if len(query) == 0 {
			return fmt.Errorf("%w: queries[%d] must not be empty", ErrInvalid, index)
		}
		if err := validateQueryKeys(query, index); err != nil {
			return err
		}
		for _, field := range alibSearchFields {
			if value, present := query[field]; present {
				if err := validateSearchValue(field, value, context, index); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func buildSearchEndpoints(
	categories []string,
	filters map[string][]string,
	queries []map[string]string,
	context searchErrorContext,
) ([]string, error) {
	endpoints := make([]string, 0, len(categories)+len(filters)+len(queries))
	seenURLs := make(map[string]struct{}, cap(endpoints))
	appendUnique := func(endpoint string) {
		if _, seen := seenURLs[endpoint]; seen {
			return
		}
		seenURLs[endpoint] = struct{}{}
		endpoints = append(endpoints, endpoint)
	}

	for _, category := range categories {
		appendUnique("https://www.alib.ru/" + category + ".phtml?tnew=7")
	}
	for _, field := range alibSearchFields {
		for _, value := range filters[field] {
			endpoint, err := buildSearchURL(map[string]string{field: value}, context)
			if err != nil {
				return nil, err
			}
			appendUnique(endpoint)
		}
	}
	for _, query := range queries {
		endpoint, err := buildSearchURL(query, context)
		if err != nil {
			return nil, err
		}
		appendUnique(endpoint)
	}

	return endpoints, nil
}

func validateFilterKeys(filters map[string][]string, context searchErrorContext) error {
	known := make(map[string]struct{}, len(alibSearchFields))
	for _, field := range alibSearchFields {
		known[field] = struct{}{}
	}
	for _, field := range slices.Sorted(maps.Keys(filters)) {
		if _, ok := known[field]; !ok {
			return invalidSearchValue(context, field, "is not a supported Alib form field")
		}
	}
	return nil
}

func validateQueryKeys(query map[string]string, index int) error {
	known := make(map[string]struct{}, len(alibSearchFields))
	for _, field := range alibSearchFields {
		known[field] = struct{}{}
	}
	for _, field := range slices.Sorted(maps.Keys(query)) {
		if _, ok := known[field]; !ok {
			return fmt.Errorf("%w: queries[%d].%s is not a supported Alib form field", ErrInvalid, index, field)
		}
	}
	return nil
}

func validateSearchValue(field, value string, context searchErrorContext, index int) error {
	if strings.TrimSpace(value) == "" {
		return invalidSearchValue(context, field, fmt.Sprintf("item %d must not be empty", index))
	}
	switch field {
	case searchFieldNoReprint, searchFieldNoEngraving, searchFieldPhotoOnly:
		if value != "da" {
			return invalidSearchValue(context, field, "must be da")
		}
	case searchFieldPriceRange:
		if !oneOf(value, "1", "2", "3", "4", "5") {
			return invalidSearchValue(context, field, "must be between 1 and 5")
		}
	case searchFieldCategory:
		if _, ok := alibTipfindValues[value]; !ok {
			return invalidSearchValue(context, field, "contains an unknown rubric")
		}
	case searchFieldSort:
		if !oneOf(value, "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10") {
			return invalidSearchValue(context, field, "must be between 0 and 10")
		}
	}
	return nil
}

func buildSearchURL(values map[string]string, context searchErrorContext) (string, error) {
	parameters := make([]string, 0, len(values)+1)
	hasLastDays := false
	for _, field := range alibSearchFields {
		value, present := values[field]
		if !present {
			continue
		}
		if field == searchFieldLastDays {
			hasLastDays = true
		}
		encoded, err := encodeSearchValue(field, value, context)
		if err != nil {
			return "", err
		}
		parameters = append(parameters, field+"="+url.QueryEscape(string(encoded)))
	}
	if !hasLastDays {
		encoded, err := encodeSearchValue(searchFieldLastDays, "7", context)
		if err != nil {
			return "", err
		}
		parameters = append(parameters, searchFieldLastDays+"="+url.QueryEscape(string(encoded)))
	}

	return "https://alib.ru/findp.php4?" + strings.Join(parameters, "&"), nil
}

func encodeSearchValue(field, value string, context searchErrorContext) ([]byte, error) {
	encoded, err := charmap.Windows1251.NewEncoder().Bytes([]byte(value))
	if err != nil {
		return nil, fmt.Errorf(
			"%w: %s value %q cannot be represented in Windows-1251: %w",
			ErrInvalid,
			context.field(field),
			value,
			err,
		)
	}
	return encoded, nil
}

func invalidSearchValue(context searchErrorContext, field, reason string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalid, context.field(field), reason)
}

func (context searchErrorContext) field(field string) string {
	if name, ok := context.fields[field]; ok {
		return name
	}
	return field
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func makeSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}
