package service

import (
	"reflect"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/archive"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

func formulaItemFromLive(formula *domain.Formula) PlanItemView {
	return PlanItemView{
		Kind:     archive.KindFormula,
		ID:       formula.ID,
		Name:     formula.Name,
		State:    string(formula.State),
		Version:  formula.Version,
		Location: locationLive,
	}
}

func formulaItemFromArchive(formula *domain.Formula, location string) PlanItemView {
	item := formulaItemFromLive(formula)
	item.Location = location
	return item
}

func vesselItemFromLive(vessel *domain.Vessel) PlanItemView {
	return PlanItemView{
		Kind:     archive.KindVessel,
		ID:       vessel.ID,
		Code:     vessel.Code,
		State:    string(vessel.State),
		Version:  vessel.Version,
		Location: locationLive,
	}
}

func vesselItemFromArchive(vessel *domain.Vessel, location string) PlanItemView {
	item := vesselItemFromLive(vessel)
	item.Location = location
	return item
}

func runItemFromLive(run *domain.FermentationRun) PlanItemView {
	return PlanItemView{
		Kind:             archive.KindRun,
		ID:               run.ID,
		Code:             run.Code,
		State:            string(run.State),
		Version:          run.Version,
		Location:         locationLive,
		ObservationCount: len(run.Observations),
	}
}

func runItemFromArchive(run *domain.FermentationRun, location string) PlanItemView {
	item := runItemFromLive(run)
	item.Location = location
	return item
}

func formulaEqual(a, b domain.Formula) bool {
	return reflect.DeepEqual(a, b)
}

func vesselEqual(a, b *domain.Vessel) bool {
	if a == nil || b == nil {
		return a == b
	}
	return reflect.DeepEqual(a, b)
}

func reflectDeepEqualRun(a, b *domain.FermentationRun) bool {
	if a == nil || b == nil {
		return a == b
	}
	return reflect.DeepEqual(a, b)
}
