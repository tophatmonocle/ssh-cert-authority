package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/cloudtools/ssh-cert-authority/util"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const buildVersion string = "dev"

type listResponseElement struct {
	Environment string
	Reason      string
	CertBlob    string
}

type certRequestResponse map[string]listResponseElement

func main() {
	var environment, certRequestID string

	home := os.Getenv("HOME")
	if home == "" {
		home = "/"
	}
	configPath := home + "/.ssh_ca/signer_config.json"

	flag.StringVar(&environment, "environment", "", "The environment you want (e.g. prod).")
	flag.StringVar(&configPath, "configPath", configPath, "Path to config json.")
	flag.StringVar(&certRequestID, "cert-request-id", certRequestID, "ID of cert request.")
	printVersion := flag.Bool("version", false, "Print the version and exit")
	flag.Parse()

	if *printVersion {
		fmt.Printf("sign_cert v.%s\n", buildVersion)
		os.Exit(0)
	}

	allConfig := make(map[string]ssh_ca_util.SignerConfig)
	err := ssh_ca_util.LoadConfig(configPath, &allConfig)
	if err != nil {
		fmt.Println("Load Config failed:", err)
		os.Exit(1)
	}

	if certRequestID == "" {
		fmt.Println("Specify --cert-request-id")
		os.Exit(1)
	}

	if len(allConfig) > 1 && environment == "" {
		fmt.Println("You must tell me which environment to use.", len(allConfig))
		os.Exit(1)
	}
	if len(allConfig) == 1 && environment == "" {
		for environment = range allConfig {
			// lame way of extracting first and only key from a map?
		}
	}
	config, ok := allConfig[environment]
	if !ok {
		fmt.Println("Requested environment not found in config file")
		os.Exit(1)
	}

	conn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK"))
	if err != nil {
		fmt.Println("Dial failed:", err)
		os.Exit(1)
	}
	sshAgent := agent.NewClient(conn)

	signers, err := sshAgent.Signers()
	var signer ssh.Signer
	signer = nil
	if err != nil {
		fmt.Println("No keys found in agent, can't sign request, bailing.")
		fmt.Println("ssh-add the private half of the key you want to use.")
		os.Exit(1)
	} else {
		for i := range signers {
			signerFingerprint := ssh_ca_util.MakeFingerprint(signers[i].PublicKey().Marshal())
			if signerFingerprint == config.KeyFingerprint {
				signer = signers[i]
				break
			}
		}
	}
	if signer == nil {
		fmt.Println("ssh-add the private half of the key you want to use.")
		os.Exit(1)
	}

	requestParameters := make(url.Values)
	requestParameters["environment"] = make([]string, 1)
	requestParameters["environment"][0] = environment
	requestParameters["certRequestId"] = make([]string, 1)
	requestParameters["certRequestId"][0] = certRequestID
	getResp, err := http.Get(config.SignerUrl + "cert/requests?" + requestParameters.Encode())
	if err != nil {
		fmt.Println("Didn't get a valid response", err)
		os.Exit(1)
	}
	getRespBuf, err := ioutil.ReadAll(getResp.Body)
	if err != nil {
		fmt.Println("Error reading response body", err)
		os.Exit(1)
	}
	getResp.Body.Close()
	if getResp.StatusCode != 200 {
		fmt.Println("Error getting that request id:", string(getRespBuf))
		os.Exit(1)
	}
	getResponse := make(certRequestResponse)
	err = json.Unmarshal(getRespBuf, &getResponse)
	if err != nil {
		fmt.Println("Unable to unmarshall response", err)
		os.Exit(1)
	}
	parseableCert := []byte("ssh-rsa-cert-v01@openssh.com " + getResponse[certRequestID].CertBlob)
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(parseableCert)
	if err != nil {
		fmt.Println("Trouble parsing response", err)
		os.Exit(1)
	}
	cert := *pubKey.(*ssh.Certificate)
	fmt.Printf("This cert is for the %s environment\n", getResponse[certRequestID].Environment)
	fmt.Println("Reason:", getResponse[certRequestID].Reason)
	ssh_ca_util.PrintForInspection(cert)
	fmt.Printf("Type 'yes' if you'd like to sign this cert request ")
	reader := bufio.NewReader(os.Stdin)
	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)
	if text != "yes" && text != "YES" {
		os.Exit(0)
	}

	err = cert.SignCert(rand.Reader, signer)
	if err != nil {
		fmt.Println("Error signing:", err)
		os.Exit(1)
	}

	signedRequest := cert.Marshal()

	requestParameters = make(url.Values)
	requestParameters["cert"] = make([]string, 1)
	requestParameters["cert"][0] = base64.StdEncoding.EncodeToString(signedRequest)
	requestParameters["environment"] = make([]string, 1)
	requestParameters["environment"][0] = environment
	resp, err := http.PostForm(config.SignerUrl+"cert/requests/"+certRequestID, requestParameters)
	if err != nil {
		fmt.Println("Error sending request to signer daemon:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		fmt.Println("Signature accepted by server.")
	} else {
		fmt.Println("Cert signature not accepted.")
		fmt.Println("HTTP status", resp.Status)
		respBuf, _ := ioutil.ReadAll(resp.Body)
		fmt.Println(string(respBuf))
		os.Exit(1)
	}

}