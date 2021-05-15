package aws_utils

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	route53 "github.com/aws/aws-sdk-go/service/route53"
	log "github.com/sirupsen/logrus"
)

func NewRoute53Api() Route53Api {
	return &Route53Manager{
		session:     GetEnvSession(),
		nameservers: map[string][]string{},
	}
}
func NewRecordName(rawQuery string) (RecordStream, error) {
	splitted, err := StripRecord(rawQuery)
	if err != nil {
		return nil, err
	}
	log.WithField("parsedRecord", splitted).Debug("record value after strip")
	if splitted[len(splitted)-1] == "" {
		splitted = splitted[:len(splitted)-1]
	}
	return &RecordName{
		rawURL:      rawQuery,
		parsedURL:   strings.Join(splitted, "."),
		splittedURL: splitted,
		hasWildCard: splitted[0] == WildCard,
	}, nil
}

func (r *RecordName) HasWildCard() bool {
	return r.hasWildCard
}
func (r *RecordName) GetParsedURL() string {
	return r.parsedURL
}
func (r *RecordName) GetWithWildCard() (string, error) {
	if len(r.splittedURL) == 0 {
		return "", errors.New("InvalidDNSWildCardTest")
	}
	withWc := strings.Replace(r.parsedURL, r.splittedURL[0], WildCard, 1)
	return withWc, nil
}
func (r *RecordName) IsEqual(other string) bool {

	if other == r.parsedURL+"." || other == r.parsedURL {
		return true
	}
	if !r.hasWildCard && strings.HasPrefix(other, WildCard) {

		otherWithNoWc := strings.Replace(other, WildCard, r.splittedURL[0], 1)

		if otherWithNoWc == r.parsedURL+"." || otherWithNoWc == r.parsedURL {
			return true
		}
	}

	return false
}
func (r *RecordName) GetAllOptionsForZoneName() ([]string, error) {
	opts := []string{}
	size := len(r.splittedURL)

	if size == 1 && r.hasWildCard {
		return nil, errors.New("InvalidRecordWildCard")
	}
	// a -> [a]
	if size == 1 {
		return append(opts, r.parsedURL), nil
	}
	// *.a -> [a]
	if size == 2 && r.hasWildCard {
		return []string{r.splittedURL[size-1]}, nil
	}
	// a.b -> [a.b,  b]
	if size == 2 {
		opts = append(opts, r.parsedURL, r.splittedURL[1])
		return opts, nil
	}
	// *.a.b -> [a.b, b]
	if size == 3 && r.hasWildCard {
		{
		}
		opts = append(opts, strings.Join(r.splittedURL[1:size], "."))
		opts = append(opts, r.splittedURL[size-1])
		return opts, nil
	}
	// a.b.c -> [a.b.c, b.c, c ]
	if size == 3 {
		opts = append(opts, r.parsedURL)
		opts = append(opts, strings.Join(r.splittedURL[1:size], "."))
		opts = append(opts, r.splittedURL[size-1])
		return opts, nil
	}
	// *.a.b.c.d -> [a.b.c.d, ... , d]
	// a.b.c.d.e -> [a.b.c.d.e, ..., e]
	for idx, _ := range r.splittedURL {
		if idx == 0 && r.hasWildCard {
			continue
		}
		testRecord := strings.Join(r.splittedURL[idx:size], ".")
		log.WithField("record", testRecord).Debug("potential hosted zone name")
		opts = append(opts, testRecord)
	}
	return opts, nil
}

func (r53m *Route53Manager) GetRegion() string {
	return *r53m.session.Config.Region
}

func (r53m *Route53Manager) client() *route53.Route53 {
	if r53m.r53client == nil {
		r53m.r53client = route53.New(r53m.session)
		return r53m.r53client
	}
	return r53m.r53client
}

// works only for public zones
func (r53 *Route53Manager) TestDNSAnswer(hostedZoneId, recordName, recordType string) (*route53.TestDNSAnswerOutput, error) {
	zoneId := strings.TrimLeft(hostedZoneId, "/hostedzone/")
	c := r53.client()
	input := &route53.TestDNSAnswerInput{
		RecordType:   aws.String(recordType),
		RecordName:   aws.String(recordName),
		HostedZoneId: aws.String(zoneId),
	}
	output, err := c.TestDNSAnswer(input)
	if err != nil {
		log.WithError(err).Error("failed checking dns anser for record")
	}
	return output, nil
}

// stripRecord
// i.e https://example.com/p/a?ok=11&not=23
// into example.com
func StripRecord(fullRecord string) ([]string, error) {
	if !strings.HasPrefix(fullRecord, "http://") && !strings.HasPrefix(fullRecord, "https://") {
		fullRecord = fmt.Sprintf("http://%s", fullRecord)
	}
	u, err := url.Parse(fullRecord)
	if err != nil {
		return nil, err
	}
	return strings.Split(u.Hostname(), "."), nil
}

// gets hosted zone nameservers
// gets record name nameservers (nslookup)
// compares them
func (r53m *Route53Manager) isNSMatchRecord(hosedZone *route53.HostedZone, recordName string) (bool, error) {
	hns, err := r53m.GetHZNameservers(*hosedZone.Id)

	if err != nil {
		log.WithError(err).Error("failed getting hosted zone nameservers, abborting. to skip verification use flag --ns-skip")
		return false, err
	}

	rns, err := r53m.GetNameservers(recordName)

	if err != nil {
		log.WithError(err).Error("failed getting domain address nameservers, abborting. to skip verification use flag --ns-skip")
		return false, err
	}

	if !r53m.IsNSMatch(hns, rns) {
		log.Info("record found in hosted zone but nameserver dont match, continuing search, to skip verification use flag --ns-skip")
		return false, errors.New("ErrNoNSMatch")
	}

	return true, nil
}
func (r53m *Route53Manager) GetRecordSetAliases(recordName string, skipNSVerification bool) (*GetRecordAliasesResult, error) {
	recordStream, err := NewRecordName(recordName)
	if err != nil {
		panic(err)
	}
	recordName = recordStream.GetParsedURL()
	optionalHostedZone, err := recordStream.GetAllOptionsForZoneName()
	if err != nil {
		panic(err)
	}
	log.WithField("possible_hosted_zones", optionalHostedZone).Debug("checking the following hosted zones for the record")

	for _, hzName := range optionalHostedZone {
		hosedZone, recordSets, err := r53m.LookupRecord(hzName, recordName, recordStream)
		// if record not found in current hosted zone
		if err != nil || recordSets == nil {
			log.WithField("hostedZoneTested", hzName).Debug("records not found in zone, checking next")
			continue
		}
		// if record set found but have different nameservers uppon nslookup
		if !skipNSVerification {
			if match, err := r53m.isNSMatchRecord(hosedZone, recordName); err != nil || !match {
				continue
			}
		}
		return &GetRecordAliasesResult{Region: r53m.GetRegion(), Records: recordSets, HostedZone: hosedZone, Stream: recordStream}, nil
	}
	return nil, errors.New("NoRecordMatchFound")
}

func (r53m *Route53Manager) getRecordsAliasesAndFilter(recordName, zoneId string, recordStream RecordStream) ([]*route53.ResourceRecordSet, error) {
	result := []*route53.ResourceRecordSet{}
	recordSets, err := r53m.getRecordAliases(recordName, zoneId)
	if err != nil {
		return nil, err
	}
	// check for specific records of the query
	for _, rs := range recordSets {
		log.WithField("dns", *rs.Name).Debug("inspectig record")
		if recordStream.IsEqual(*rs.Name) {
			result = append(result, rs)
		}
	}
	return result, nil
}

// LookupRecord query for potential hosted zones
func (r53m *Route53Manager) LookupRecord(hzName, record string, recordName RecordStream) (*route53.HostedZone, []*route53.ResourceRecordSet, error) {
	result := []*route53.ResourceRecordSet{}
	// get zones
	optionalHostedZones, err := r53m.GetHostedZonesFromDns(hzName)
	if err != nil || len(optionalHostedZones) == 0 {
		return nil, nil, err
	}

	// check match in hosted zones
	for _, hz := range optionalHostedZones {
		if *hz.Name == hzName+"." {
			log.WithField("name", *hz.Name).Debug("hosted zone found!")
			zoneId := hz.Id
			// get records inside hosted zone

			filteredRecords, err := r53m.getRecordsAliasesAndFilter(record, *zoneId, recordName)
			if err != nil {
				return nil, nil, err
			}
			if len(filteredRecords) == 0 && !recordName.HasWildCard() {
				recWithWc, err := recordName.GetWithWildCard()
				if err != nil {
					return nil, nil, err
				}
				filteredRecords, err = r53m.getRecordsAliasesAndFilter(recWithWc, *zoneId, recordName)
				if err != nil {
					return nil, nil, err
				}
			}
			result = append(result, filteredRecords...)
			return hz, result, nil
		}
	}
	return nil, nil, errors.New("LookupNotFoundErr")
}

// getRecordAliases will return all record within a hosted zone that match the record name and also the rest
func (r53m *Route53Manager) getRecordAliases(recordName, zoneId string) ([]*route53.ResourceRecordSet, error) {
	log.WithField("recordName", recordName).Debug("listing resource sets in aws r53")

	c := r53m.client()
	input := &route53.ListResourceRecordSetsInput{
		HostedZoneId:    aws.String(zoneId), // Required
		StartRecordName: aws.String(recordName),
	}
	output, err := c.ListResourceRecordSets(input)
	if err != nil {
		return nil, err
	}
	return output.ResourceRecordSets, nil
}

func (r53m *Route53Manager) GetHostedZonesFromDns(recordName string) ([]*route53.HostedZone, error) {
	c := r53m.client()
	input := &route53.ListHostedZonesByNameInput{
		DNSName: aws.String(recordName),
	}
	output, err := c.ListHostedZonesByName(input)
	if err != nil {
		return nil, err
	}
	return output.HostedZones, nil
}

// given a domain address do nslookup
func (r53m *Route53Manager) GetNameservers(recordName string) ([]string, error) {

	logger := log.WithField("recordName", recordName)

	if val, exist := r53m.nameservers[recordName]; exist {
		return val, nil
	}

	logger.Debug("performing domain address nameserver lookup")

	nameserver, err := net.LookupNS(recordName + ".")

	if err != nil {
		return nil, err
	}

	var nsResult []string
	for _, ns := range nameserver {
		nsResult = append(nsResult, strings.TrimRight(ns.Host, "."))
	}
	log.WithField("ns", nsResult).Debug("found nameservers for domain address")
	return nsResult, nil
}

// given hosted zone id find the nameservers
func (r53m *Route53Manager) GetHZNameservers(hzId string) ([]string, error) {
	hzId = strings.TrimLeft(hzId, "/hostedzone/")

	logger := log.WithField("hostedZoneId", hzId)

	if val, exist := r53m.nameservers[hzId]; exist {
		return val, nil
	}

	logger.Debug("performing hosted zone nameserver lookup")

	c := r53m.client()

	i := &route53.GetHostedZoneInput{
		Id: aws.String(hzId),
	}

	o, err := c.GetHostedZone(i)

	if err != nil || o == nil || o.DelegationSet == nil {
		return nil, err
	}
	var nsResult []string
	for _, ns := range o.DelegationSet.NameServers {
		nsResult = append(nsResult, strings.TrimRight(*ns, "."))
	}
	r53m.nameservers[hzId] = nsResult

	log.WithField("ns", nsResult).Debug("found nameservers for hosted zone")

	return nsResult, nil
}

func (r53m *Route53Manager) IsNSMatch(ns1, ns2 []string) bool {

	nsCounter := map[string]int{}
	for _, n := range ns1 {
		if n != "" {
			nsCounter[n] += 1
		}
	}
	for _, n := range ns2 {
		if n != "" {
			nsCounter[n] += 1
		}
	}

	for n, c := range nsCounter {
		if c > 1 {
			log.WithField("ns", n).Debug("found ns match")
			return true
		}
	}

	return false
}
