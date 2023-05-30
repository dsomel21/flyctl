package migrate_to_v2

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/samber/lo"
	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/internal/machine"
)

func (m *v2PlatformMigrator) createLaunchMachineInput(oldAllocID string, skipLaunch bool) (*api.LaunchMachineInput, error) {
	taskName := ""

	if len(m.oldAllocs) > 0 {
		taskName = m.oldAllocs[0].TaskName
	} else {
		taskName = "app"
	}

	mConfig, err := m.appConfig.ToMachineConfig(taskName, nil)
	if err != nil {
		return nil, err
	}

	guest, ok := m.machineGuests[mConfig.ProcessGroup()]
	if !ok {
		return nil, fmt.Errorf("no guest found for process '%s'", mConfig.ProcessGroup())
	}

	mConfig.Mounts = nil
	mConfig.Guest = guest
	mConfig.Image = m.img
	mConfig.Metadata[api.MachineConfigMetadataKeyFlyReleaseId] = m.releaseId
	mConfig.Metadata[api.MachineConfigMetadataKeyFlyReleaseVersion] = strconv.Itoa(m.releaseVersion)
	if oldAllocID != "" {
		mConfig.Metadata[api.MachineConfigMetadataKeyFlyPreviousAlloc] = oldAllocID
	}

	if m.isPostgres {
		mConfig.Env["FLY_CONSUL_URL"] = m.pgConsulUrl
		mConfig.Metadata[api.MachineConfigMetadataKeyFlyManagedPostgres] = "true"
	}

	// We have manual overrides for some regions with the names <region>2 e.g ams2, iad2.
	// These cause migrations to fail. Here we handle that specific case.
	if m.appConfig == nil {
		// FIXME better error message here
		return nil, fmt.Errorf("Could not find app config")
	}

	region := m.appConfig.PrimaryRegion
	if strings.HasSuffix(region, "2") {
		region = region[0:3]
	}

	launchMachineInput := api.LaunchMachineInput{
		Config:     mConfig,
		Region:     region,
		SkipLaunch: skipLaunch,
	}

	return &launchMachineInput, nil
}

func (m *v2PlatformMigrator) resolveMachineFromAlloc(alloc *api.AllocationStatus) (*api.LaunchMachineInput, error) {
	return m.createLaunchMachineInput(alloc.ID, false)
}

func (m *v2PlatformMigrator) prepMachinesToCreate(ctx context.Context) (err error) {
	m.newMachinesInput, err = m.resolveMachinesFromAllocs()
	if err != nil {
		return err
	}

	err = m.prepAutoscaleMachinesToCreate(ctx)
	return err
}

func (m *v2PlatformMigrator) prepAutoscaleMachinesToCreate(ctx context.Context) error {
	// If the service being migrated doesn't use autoscaling, just return nil
	if m.autoscaleConfig == nil {
		return nil
	}

	// Create as many machines as necessary to be within the minimum count required
	for i := len(m.newMachinesInput); i < m.autoscaleConfig.MinCount; i += 1 {
		launchMachineInput, err := m.createLaunchMachineInput("", false)
		if err != nil {
			return fmt.Errorf("could not create machine to reach autoscale minimum count: %s", err)
		}

		m.newMachinesInput = append(m.newMachinesInput, launchMachineInput)
	}

	// Create the rest of the machines that app will use, but have them stopped by default
	for i := len(m.newMachinesInput); i < m.autoscaleConfig.MaxCount; i += 1 {
		launchMachineInput, err := m.createLaunchMachineInput("", true)
		if err != nil {
			return fmt.Errorf("could not create machine to reach autoscale minimum count: %s", err)
		}

		m.newMachinesInput = append(m.newMachinesInput, launchMachineInput)
	}

	for _, input := range m.newMachinesInput {
		for _, service := range input.Config.Services {
			service.MinMachinesRunning = &m.autoscaleConfig.MinCount
			service.Autostart = api.BoolPointer(true)
			service.Autostop = api.BoolPointer(true)
		}
	}

	return nil
}

func (m *v2PlatformMigrator) resolveMachinesFromAllocs() ([]*api.LaunchMachineInput, error) {
	var res []*api.LaunchMachineInput
	for _, alloc := range m.oldAllocs {
		mach, err := m.resolveMachineFromAlloc(alloc)
		if err != nil {
			return nil, err
		}
		res = append(res, mach)
	}
	return res, nil
}

type createdMachine struct {
	machine       *api.Machine
	expectedState string
}

func (m *v2PlatformMigrator) createMachines(ctx context.Context) error {
	var newlyCreatedMachines []createdMachine
	defer func() {
		m.recovery.machinesCreated = make([]*api.Machine, 0)

		for _, createdMachine := range newlyCreatedMachines {
			m.recovery.machinesCreated = append(m.recovery.machinesCreated, createdMachine.machine)
		}
	}()

	for _, machineInput := range m.newMachinesInput {
		if m.isPostgres && m.targetImg != "" {
			machineInput.Config.Image = m.targetImg
		}

		// Assign volume
		if nv, ok := lo.Find(m.createdVolumes, func(v *NewVolume) bool {
			return v.previousAllocId == machineInput.Config.Metadata[api.MachineConfigMetadataKeyFlyPreviousAlloc]
		}); ok {
			machineInput.Config.Mounts = []api.MachineMount{{
				Name:   nv.vol.Name,
				Path:   nv.mountPoint,
				Volume: nv.vol.ID,
			}}
		}
		// Launch machine
		newMachine, err := m.flapsClient.Launch(ctx, *machineInput)
		if err != nil {
			return fmt.Errorf("failed creating a machine in region %s: %w", machineInput.Region, err)
		}

		expectedState := "start"
		if machineInput.SkipLaunch {
			expectedState = "stop"
		}

		machInfo := createdMachine{
			machine:       newMachine,
			expectedState: expectedState,
		}
		newlyCreatedMachines = append(newlyCreatedMachines, machInfo)
	}

	for _, mach := range newlyCreatedMachines {
		err := machine.WaitForStartOrStop(ctx, mach.machine, mach.expectedState, time.Minute*5)
		if err != nil {
			return err
		}
	}

	newlyCreatedMachinesSet := make([]*api.Machine, 0)

	for _, createdMachine := range newlyCreatedMachines {
		newlyCreatedMachinesSet = append(newlyCreatedMachinesSet, createdMachine.machine)
	}

	m.newMachines = machine.NewMachineSet(m.flapsClient, m.io, newlyCreatedMachinesSet)
	return nil
}
