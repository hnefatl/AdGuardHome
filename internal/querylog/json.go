package querylog

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AdguardTeam/AdGuardHome/internal/aghnet"
	"github.com/AdguardTeam/AdGuardHome/internal/filtering"
	"github.com/AdguardTeam/golibs/log"
	"github.com/AdguardTeam/golibs/netutil"
	"github.com/miekg/dns"
	"golang.org/x/net/idna"
)

// TODO(a.garipov): Use a proper structured approach here.

// jobject is a JSON object alias.
type jobject = map[string]interface{}

// entriesToJSON converts query log entries to JSON.
func (l *queryLog) entriesToJSON(entries []*logEntry, oldest time.Time) (res jobject) {
	data := make([]jobject, 0, len(entries))

	// The elements order is already reversed to be from newer to older.
	for _, entry := range entries {
		jsonEntry := l.entryToJSON(entry, l.anonymizer.Load())
		data = append(data, jsonEntry)
	}

	res = jobject{
		"data":   data,
		"oldest": "",
	}
	if !oldest.IsZero() {
		res["oldest"] = oldest.Format(time.RFC3339Nano)
	}

	return res
}

func (l *queryLog) entryToJSON(entry *logEntry, anonFunc aghnet.IPMutFunc) (jsonEntry jobject) {
	var msg *dns.Msg

	if len(entry.Answer) > 0 {
		msg = new(dns.Msg)
		if err := msg.Unpack(entry.Answer); err != nil {
			log.Debug("Failed to unpack dns message answer: %s: %s", err, string(entry.Answer))
			msg = nil
		}
	}

	hostname := entry.QHost
	question := jobject{
		"type":  entry.QType,
		"class": entry.QClass,
		"name":  hostname,
	}
	if qhost, err := idna.ToUnicode(hostname); err == nil {
		if qhost != hostname && qhost != "" {
			question["unicode_name"] = qhost
		}
	} else {
		log.Debug("translating %q into unicode: %s", hostname, err)
	}

	eip := netutil.CloneIP(entry.IP)
	anonFunc(eip)

	jsonEntry = jobject{
		"reason":       entry.Result.Reason.String(),
		"elapsedMs":    strconv.FormatFloat(entry.Elapsed.Seconds()*1000, 'f', -1, 64),
		"time":         entry.Time.Format(time.RFC3339Nano),
		"client":       eip,
		"client_proto": entry.ClientProto,
		"cached":       entry.Cached,
		"upstream":     entry.Upstream,
		"question":     question,
	}
	if eip.Equal(entry.IP) {
		jsonEntry["client_info"] = entry.client
	}

	if entry.ClientID != "" {
		jsonEntry["client_id"] = entry.ClientID
	}

	if msg != nil {
		jsonEntry["status"] = dns.RcodeToString[msg.Rcode]

		opt := msg.IsEdns0()
		dnssecOk := false
		if opt != nil {
			dnssecOk = opt.Do()
		}

		jsonEntry["answer_dnssec"] = dnssecOk
	}

	jsonEntry["rules"] = resultRulesToJSONRules(entry.Result.Rules)

	if len(entry.Result.Rules) > 0 && len(entry.Result.Rules[0].Text) > 0 {
		jsonEntry["rule"] = entry.Result.Rules[0].Text
		jsonEntry["filterId"] = entry.Result.Rules[0].FilterListID
	}

	if len(entry.Result.ServiceName) != 0 {
		jsonEntry["service_name"] = entry.Result.ServiceName
	}

	answers := answerToMap(msg)
	if answers != nil {
		jsonEntry["answer"] = answers
	}

	if len(entry.OrigAnswer) != 0 {
		a := new(dns.Msg)
		err := a.Unpack(entry.OrigAnswer)
		if err == nil {
			answers = answerToMap(a)
			if answers != nil {
				jsonEntry["original_answer"] = answers
			}
		} else {
			log.Debug("Querylog: msg.Unpack(entry.OrigAnswer): %s: %s", err, string(entry.OrigAnswer))
		}
	}

	return jsonEntry
}

func resultRulesToJSONRules(rules []*filtering.ResultRule) (jsonRules []jobject) {
	jsonRules = make([]jobject, len(rules))
	for i, r := range rules {
		jsonRules[i] = jobject{
			"filter_list_id": r.FilterListID,
			"text":           r.Text,
		}
	}

	return jsonRules
}

type dnsAnswer struct {
	Type  string `json:"type"`
	Value string `json:"value"`
	TTL   uint32 `json:"ttl"`
}

func answerToMap(a *dns.Msg) (answers []*dnsAnswer) {
	if a == nil || len(a.Answer) == 0 {
		return nil
	}

	answers = make([]*dnsAnswer, 0, len(a.Answer))
	for _, k := range a.Answer {
		header := k.Header()
		answer := &dnsAnswer{
			Type: dns.TypeToString[header.Rrtype],
			TTL:  header.Ttl,
		}

		// Some special treatment for some well-known types.
		//
		// TODO(a.garipov): Consider just calling String() for everyone
		// instead.
		switch v := k.(type) {
		case nil:
			// Probably unlikely, but go on.
		case *dns.A:
			answer.Value = v.A.String()
		case *dns.AAAA:
			answer.Value = v.AAAA.String()
		case *dns.MX:
			answer.Value = fmt.Sprintf("%v %v", v.Preference, v.Mx)
		case *dns.CNAME:
			answer.Value = v.Target
		case *dns.NS:
			answer.Value = v.Ns
		case *dns.SPF:
			answer.Value = strings.Join(v.Txt, "\n")
		case *dns.TXT:
			answer.Value = strings.Join(v.Txt, "\n")
		case *dns.PTR:
			answer.Value = v.Ptr
		case *dns.SOA:
			answer.Value = fmt.Sprintf("%v %v %v %v %v %v %v", v.Ns, v.Mbox, v.Serial, v.Refresh, v.Retry, v.Expire, v.Minttl)
		case *dns.CAA:
			answer.Value = fmt.Sprintf("%v %v \"%v\"", v.Flag, v.Tag, v.Value)
		case *dns.HINFO:
			answer.Value = fmt.Sprintf("\"%v\" \"%v\"", v.Cpu, v.Os)
		case *dns.RRSIG:
			answer.Value = fmt.Sprintf("%v %v %v %v %v %v %v %v %v", dns.TypeToString[v.TypeCovered], v.Algorithm, v.Labels, v.OrigTtl, v.Expiration, v.Inception, v.KeyTag, v.SignerName, v.Signature)
		default:
			answer.Value = v.String()
		}

		answers = append(answers, answer)
	}

	return answers
}
