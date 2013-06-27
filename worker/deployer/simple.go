// Copyright 2012, 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package deployer

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"strings"

	"launchpad.net/juju-core/environs/agent"
	"launchpad.net/juju-core/log/syslog"
	"launchpad.net/juju-core/state"
	"launchpad.net/juju-core/state/api"
	"launchpad.net/juju-core/upstart"
	"launchpad.net/juju-core/version"
)

// SimpleContext is a Context that manages unit deployments via upstart
// jobs on the local system.
type SimpleContext struct {

	// Addresser is used to get the current state server addresses at the time
	// the given unit is deployed.
	addresser Addresser

	// CACert holds the CA certificate that will be used
	// to validate the state server's certificate, in PEM format.
	caCert []byte

	// DeployerTag identifies the agent on whose behalf this context is running.
	deployerTag string

	// InitDir specifies the directory used by upstart on the local system.
	// It is typically set to "/etc/init".
	initDir string

	// DataDir specifies the directory used by juju to store its state. It
	// is typically set to "/var/lib/juju".
	dataDir string

	// LogDir specifies the directory to which installed units will write
	// their log files. It is typically set to "/var/log/juju".
	logDir string

	// sysLogConfigDir specifies the directory to which the syslog conf file
	// will be written. It is set for testing and left empty for production, in
	// which case the system default is used, typically /etc/rsyslog.d
	syslogConfigDir string

	// syslogConfigPath is the full path name of the syslog conf file.
	syslogConfigPath string
}

var _ Context = (*SimpleContext)(nil)

// NewSimpleContext returns a new SimpleContext, acting on behalf of the
// specified deployer, that deploys unit agents as upstart jobs in
// "/etc/init" logging to "/var/log/juju". Paths to which agents and tools
// are installed are relative to dataDir; if dataDir is empty, it will be
// set to "/var/lib/juju".
func NewSimpleContext(dataDir string, CACert []byte, deployerTag string,
	addresser Addresser) *SimpleContext {
	if dataDir == "" {
		dataDir = "/var/lib/juju"
	}
	return &SimpleContext{
		addresser:   addresser,
		caCert:      CACert,
		deployerTag: deployerTag,
		initDir:     "/etc/init",
		dataDir:     dataDir,
		logDir:      "/var/log/juju",
	}
}

func (ctx *SimpleContext) DeployUnit(unitName, initialPassword string) (err error) {
	// Check sanity.
	svc := ctx.upstartService(unitName)
	if svc.Installed() {
		return fmt.Errorf("unit %q is already deployed", unitName)
	}

	// Link the current tools for use by the new agent.
	tag := state.UnitTag(unitName)
	_, err = agent.ChangeAgentTools(ctx.dataDir, tag, version.Current)
	toolsDir := agent.ToolsDir(ctx.dataDir, tag)
	defer removeOnErr(&err, toolsDir)

	// Retrieve addresses from state.
	stateAddrs, err := ctx.addresser.Addresses()
	if err != nil {
		return err
	}
	apiAddrs, err := ctx.addresser.APIAddresses()
	if err != nil {
		return err
	}

	stateInfo := state.Info{
		Addrs:  stateAddrs,
		Tag:    tag,
		CACert: ctx.caCert,
	}
	apiInfo := api.Info{
		Addrs:  apiAddrs,
		Tag:    tag,
		CACert: ctx.caCert,
	}
	// Prepare the agent's configuration data.
	conf := &agent.Conf{
		DataDir:     ctx.dataDir,
		OldPassword: initialPassword,
		StateInfo:   &stateInfo,
		APIInfo:     &apiInfo,
	}
	if err := conf.Write(); err != nil {
		return err
	}
	defer removeOnErr(&err, conf.Dir())

	// Install an upstart job that runs the unit agent.
	logPath := path.Join(ctx.logDir, tag+".log")
	syslogConfigRenderer := syslog.NewForwardConfig(tag, stateAddrs)
	syslogConfigRenderer.ConfigDir = ctx.syslogConfigDir
	syslogConfigRenderer.ConfigFileName = fmt.Sprintf("26-juju-%s.conf", tag)
	if err := syslogConfigRenderer.Write(); err != nil {
		return err
	}
	ctx.syslogConfigPath = syslogConfigRenderer.ConfigFilePath()
	if e := syslog.Restart(); e != nil {
		logger.Warningf("installer: cannot restart syslog daemon: %v", e)
	}
	defer removeOnErr(&err, ctx.syslogConfigPath)

	cmd := strings.Join([]string{
		path.Join(toolsDir, "jujud"), "unit",
		"--data-dir", conf.DataDir,
		"--unit-name", unitName,
		"--debug", // TODO: propagate debug state sensibly
	}, " ")
	uconf := &upstart.Conf{
		Service: *svc,
		Desc:    "juju unit agent for " + unitName,
		Cmd:     cmd,
		Out:     logPath,
	}
	return uconf.Install()
}

func (ctx *SimpleContext) RecallUnit(unitName string) error {
	svc := ctx.upstartService(unitName)
	if !svc.Installed() {
		return fmt.Errorf("unit %q is not deployed", unitName)
	}
	if err := svc.Remove(); err != nil {
		return err
	}
	tag := state.UnitTag(unitName)
	agentDir := agent.Dir(ctx.dataDir, tag)
	if err := os.RemoveAll(agentDir); err != nil {
		return err
	}
	if e := os.Remove(ctx.syslogConfigPath); e != nil {
		logger.Warningf("installer: cannot remove %q: %v", ctx.syslogConfigPath, e)
	}
	if e := syslog.Restart(); e != nil {
		logger.Warningf("installer: cannot restart syslog daemon: %v", e)
	}
	toolsDir := agent.ToolsDir(ctx.dataDir, tag)
	return os.Remove(toolsDir)
}

var deployedRe = regexp.MustCompile("^jujud-([a-z0-9-]+):unit-([a-z0-9-]+)-([0-9]+)\\.conf$")

func (ctx *SimpleContext) DeployedUnits() ([]string, error) {
	fis, err := ioutil.ReadDir(ctx.initDir)
	if err != nil {
		return nil, err
	}
	var installed []string
	for _, fi := range fis {
		if groups := deployedRe.FindStringSubmatch(fi.Name()); len(groups) == 4 {
			if groups[1] != ctx.deployerTag {
				continue
			}
			unitName := groups[2] + "/" + groups[3]
			if !state.IsUnitName(unitName) {
				continue
			}
			installed = append(installed, unitName)
		}
	}
	return installed, nil
}

// upstartService returns an upstart.Service corresponding to the specified
// unit. Its name is badged according to the deployer name for the
// context, so as to distinguish its own jobs from those installed by other
// means.
func (ctx *SimpleContext) upstartService(unitName string) *upstart.Service {
	tag := state.UnitTag(unitName)
	svcName := "jujud-" + ctx.deployerTag + ":" + tag
	svc := upstart.NewService(svcName)
	svc.InitDir = ctx.initDir
	return svc
}

func removeOnErr(err *error, path string) {
	if *err != nil {
		if e := os.Remove(path); e != nil {
			logger.Warningf("installer: cannot remove %q: %v", path, e)
		}
	}
}
