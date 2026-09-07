package config_test

import (
	"testing"

	"github.com/kemko/alib-fetcher/internal/config"

	"github.com/stretchr/testify/require"
)

func TestBuildSearchURLs_builds_all_form_fields_in_form_order(t *testing.T) {
	// Given
	query := map[string]string{
		"sortby":    "10",
		"tipfind":   "t19poem",
		"sumfind":   "5",
		"minus":     "bad",
		"fotoonly":  "da",
		"nograv":    "da",
		"noreprint": "da",
		"lday":      "14",
		"gorod":     "City",
		"bsonly":    "Seller",
		"sod":       "Notes",
		"cena2":     "2",
		"cena1":     "1",
		"god2":      "2000",
		"god1":      "1900",
		"isbnp":     "ISBN",
		"gorodiz":   "Moscow",
		"izdat":     "Publisher",
		"seria":     "Series",
		"title":     "Title",
		"author":    "Author",
	}

	// When
	got, err := config.BuildSearchURLs(nil, nil, []map[string]string{query})

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://alib.ru/findp.php4?author=Author&title=Title&seria=Series&izdat=Publisher&gorodiz=Moscow&isbnp=ISBN&god1=1900&god2=2000&cena1=1&cena2=2&sod=Notes&bsonly=Seller&gorod=City&lday=14&noreprint=da&nograv=da&fotoonly=da&minus=bad&sumfind=5&tipfind=t19poem&sortby=10",
	}, got)
}

func TestBuildSearchURLs_builds_independent_filters_and_preserves_source_order(t *testing.T) {
	// Given
	filters := map[string][]string{
		"sortby": {"10"},
		"seria":  {"Первая серия", "Вторая серия"},
		"author": {"Стругацкие"},
	}

	// When
	got, err := config.BuildSearchURLs([]string{"tramka", "tramka", "deti"}, filters, []map[string]string{
		{"title": "Понедельник"},
	})

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://www.alib.ru/tramka.phtml?tnew=7",
		"https://www.alib.ru/deti.phtml?tnew=7",
		"https://alib.ru/findp.php4?author=%D1%F2%F0%F3%E3%E0%F6%EA%E8%E5&lday=7",
		"https://alib.ru/findp.php4?seria=%CF%E5%F0%E2%E0%FF+%F1%E5%F0%E8%FF&lday=7",
		"https://alib.ru/findp.php4?seria=%C2%F2%EE%F0%E0%FF+%F1%E5%F0%E8%FF&lday=7",
		"https://alib.ru/findp.php4?sortby=10&lday=7",
		"https://alib.ru/findp.php4?title=%CF%EE%ED%E5%E4%E5%EB%FC%ED%E8%EA&lday=7",
	}, got)
}

func TestBuildSearchURLs_encodes_values_without_query_injection(t *testing.T) {
	// Given
	search := config.SearchConfig{
		Queries: []map[string]string{{
			"title": "A&B, \"C\"",
			"seria": "Литературные памятники",
		}},
	}

	// When
	got, err := config.BuildAlibURLs(search)

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://alib.ru/findp.php4?title=A%26B%2C+%22C%22&seria=%CB%E8%F2%E5%F0%E0%F2%F3%F0%ED%FB%E5+%EF%E0%EC%FF%F2%ED%E8%EA%E8&lday=7",
	}, got)
}

func TestBuildSearchURLs_rejects_invalid_sources_and_values(t *testing.T) {
	// Given
	_, err := config.BuildSearchURLs([]string{"tram-ka"}, nil, nil)
	require.ErrorIs(t, err, config.ErrInvalid)
	require.ErrorContains(t, err, "invalid category")

	names := []string{
		"no sources", "unknown filter", "empty filter list", "empty query", "unknown query field", "empty value",
		"invalid flag", "invalid price range", "invalid rubric", "invalid sort", "unrepresentable value",
	}
	filters := []map[string][]string{
		nil,
		{"unknown": {"value"}},
		{"author": {}},
		nil,
		nil,
		{"author": {"  "}},
		{"fotoonly": {"yes"}},
		{"sumfind": {"6"}},
		{"tipfind": {"unknown"}},
		{"sortby": {"11"}},
		nil,
	}
	queries := [][]map[string]string{
		nil,
		nil,
		nil,
		{{}},
		{{"unknown": "value"}},
		nil,
		nil,
		nil,
		nil,
		nil,
		{{"title": "😀"}},
	}
	wants := []string{
		"categories, filters, and queries must not all be empty",
		"unknown is not a supported",
		"author must contain at least one value",
		"queries[0] must not be empty",
		"queries[0].unknown is not a supported",
		"author item 0 must not be empty",
		"fotoonly must be da",
		"sumfind must be between 1 and 5",
		"tipfind contains an unknown rubric",
		"sortby must be between 0 and 10",
		"title value \"😀\" cannot be represented in Windows-1251",
	}

	for index, name := range names {
		t.Run(name, func(t *testing.T) {
			// When
			got, buildErr := config.BuildSearchURLs(nil, filters[index], queries[index])

			// Then
			require.ErrorIs(t, buildErr, config.ErrInvalid)
			require.ErrorContains(t, buildErr, wants[index])
			require.Nil(t, got)
		})
	}
}

func TestBuildSearchURLs_validates_select_values_and_defaults_last_days(t *testing.T) {
	// When
	got, err := config.BuildSearchURLs(nil, nil, []map[string]string{{
		"noreprint": "da",
		"nograv":    "da",
		"fotoonly":  "da",
		"sumfind":   "1",
		"tipfind":   "t2mv",
		"sortby":    "0",
	}})

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://alib.ru/findp.php4?noreprint=da&nograv=da&fotoonly=da&sumfind=1&tipfind=t2mv&sortby=0&lday=7",
	}, got)
}
