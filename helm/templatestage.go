package helm

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v2"

	"github.com/appnexus/ankh/context"
	"github.com/appnexus/ankh/plan"
	"github.com/appnexus/ankh/util"
)

type TemplateStage struct {
	charts []ankh.Chart
}

func NewTemplateStage(charts []ankh.Chart) plan.Stage {
	return TemplateStage{charts: charts}
}

func (stage TemplateStage) Execute(ctx *ankh.ExecutionContext, input *string, namespace string, wildCardLabels []string) (string, error) {
	// Template, then filter.
	helmOutput, err := helmTemplate(ctx, stage.charts, namespace)
	if err != nil {
		return "", err
	}

	if !ctx.Explain {
		if len(ctx.Filters) > 0 {
			helmOutput = filterOutput(ctx, helmOutput)
		}
	}
	return helmOutput, nil
}


func templateChart(ctx *ankh.ExecutionContext, chart ankh.Chart, namespace string) (string, error) {
	currentContext := ctx.AnkhConfig.CurrentContext
	helmArgs := []string{ctx.AnkhConfig.Helm.Command, "template"}

	if namespace != "" {
		helmArgs = append(helmArgs, []string{"--namespace", namespace}...)
	}

	if currentContext.Release != "" {
		helmArgs = append(helmArgs, []string{"--name", currentContext.Release}...)
	}

	for key, val := range ctx.HelmSetValues {
		helmArgs = append(helmArgs, "--set", key+"="+val)
	}

	// Set tagKey=tagValue, if configured and present
	if chart.ChartMeta.TagKey != "" && chart.Tag != nil {
		ctx.Logger.Debugf("Setting helm value %v=%v since chart.ChartMeta.TagKey and chart.Tag are set",
			chart.ChartMeta.TagKey, *chart.Tag)
		helmArgs = append(helmArgs, "--set", chart.ChartMeta.TagKey+"="+*chart.Tag)
	}

	repository := ctx.DetermineHelmRepository(&chart.HelmRepository)
	files, err := findChartFiles(ctx, repository, chart)

	if err != nil {
		return "", err
	}

	// Load `values` from chart
	_, valuesErr := os.Stat(files.AnkhValuesPath)
	if valuesErr == nil {
		if _, err := util.CreateReducedYAMLFile(files.AnkhValuesPath, currentContext.EnvironmentClass, true); err != nil {
			return "", fmt.Errorf("unable to process ankh-values.yaml file for chart '%s': %v", chart.Name, err)
		}
		helmArgs = append(helmArgs, "-f", files.AnkhValuesPath)
	}

	// Load `resource-profiles` from chart
	_, resourceProfilesError := os.Stat(files.AnkhResourceProfilesPath)
	if resourceProfilesError == nil {
		if _, err := util.CreateReducedYAMLFile(files.AnkhResourceProfilesPath, currentContext.ResourceProfile, true); err != nil {
			return "", fmt.Errorf("unable to process ankh-resource-profiles.yaml file for chart '%s': %v", chart.Name, err)
		}
		helmArgs = append(helmArgs, "-f", files.AnkhResourceProfilesPath)
	}

	// Load `releases` from chart
	if currentContext.Release != "" {
		_, releasesError := os.Stat(files.AnkhReleasesPath)
		if releasesError == nil {
			out, err := util.CreateReducedYAMLFile(files.AnkhReleasesPath, currentContext.Release, false)
			if err != nil {
				return "", fmt.Errorf("unable to process ankh-releases.yaml file for chart '%s': %v", chart.Name, err)
			}
			if len(out) > 0 {
				helmArgs = append(helmArgs, "-f", files.AnkhReleasesPath)
			}
		}
	}

	// Load `default-values`
	if chart.DefaultValues != nil {
		defaultValuesPath := filepath.Join(files.Dir, "default-values.yaml")
		defaultValuesBytes, err := yaml.Marshal(chart.DefaultValues)
		if err != nil {
			return "", err
		}

		if err := ioutil.WriteFile(defaultValuesPath, defaultValuesBytes, 0644); err != nil {
			return "", err
		}

		helmArgs = append(helmArgs, "-f", defaultValuesPath)
	}

	// Load `values`
	if chart.Values != nil {
		values, err := util.MapSliceRegexMatch(chart.Values, currentContext.EnvironmentClass)
		if err != nil {
			return "", fmt.Errorf("Failed to load `values` for chart %v: %v", chart.Name, err)
		}
		if values != nil {
			valuesPath := filepath.Join(files.Dir, "values.yaml")
			valuesBytes, err := yaml.Marshal(values)
			if err != nil {
				return "", err
			}

			if err := ioutil.WriteFile(valuesPath, valuesBytes, 0644); err != nil {
				return "", err
			}

			helmArgs = append(helmArgs, "-f", valuesPath)
		}
	}

	// Load `resource-profiles`
	if chart.ResourceProfiles != nil {
		values, err := util.MapSliceRegexMatch(chart.ResourceProfiles, currentContext.ResourceProfile)
		if err != nil {
			return "", fmt.Errorf("Failed to load `resource-profiles` for chart %v: %v", chart.Name, err)
		}
		if values != nil {
			resourceProfilesPath := filepath.Join(files.Dir, "resource-profiles.yaml")
			resourceProfilesBytes, err := yaml.Marshal(values)

			if err != nil {
				return "", err
			}

			if err := ioutil.WriteFile(resourceProfilesPath, resourceProfilesBytes, 0644); err != nil {
				return "", err
			}

			helmArgs = append(helmArgs, "-f", resourceProfilesPath)
		}
	}

	// Load `releases`
	if chart.Releases != nil {
		values, err := util.MapSliceRegexMatch(chart.Releases, currentContext.Release)
		if err != nil {
			return "", fmt.Errorf("Failed to load `releases` for chart %v: %v", chart.Name, err)
		}
		if values != nil {
			releasesPath := filepath.Join(files.Dir, "releases.yaml")
			releasesBytes, err := yaml.Marshal(values)

			if err != nil {
				return "", err
			}

			if err := ioutil.WriteFile(releasesPath, releasesBytes, 0644); err != nil {
				return "", err
			}

			helmArgs = append(helmArgs, "-f", releasesPath)
		}
	}

	// Check if Global contains anything and append them
	if currentContext.Global != nil {
		ctx.Logger.Debugf("found global values for the current context")

		globalYamlBytes, err := yaml.Marshal(map[string]interface{}{
			"global": currentContext.Global,
		})
		if err != nil {
			return "", err
		}

		ctx.Logger.Debugf("writing global values to %s", files.GlobalPath)

		if err := ioutil.WriteFile(files.GlobalPath, globalYamlBytes, 0644); err != nil {
			return "", err
		}

		helmArgs = append(helmArgs, "-f", files.GlobalPath)
	}

	helmArgs = append(helmArgs, files.ChartDir)

	ctx.Logger.Debugf("running helm command %s", strings.Join(helmArgs, " "))

	helmCmd := execContext(helmArgs[0], helmArgs[1:]...)

	if ctx.Explain {
		out := explain(helmCmd.Args)

		// Need to strip off the final bit of the 'and chain'. Weird, but fine.
		out = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(out), "&& \\"))
		return out, nil
	}

	var stdout, stderr bytes.Buffer
	helmCmd.Stdout = &stdout
	helmCmd.Stderr = &stderr

	err = helmCmd.Run()
	var helmOutput, helmError = string(stdout.Bytes()), string(stderr.Bytes())
	if err != nil {
		outputMsg := ""
		if len(helmError) > 0 {
			outputMsg = fmt.Sprintf(" -- the helm process had the following output on stderr:\n%s", helmError)
		}
		return "", fmt.Errorf("error running the helm command: %v%v", err, outputMsg)
	}

	return string(helmOutput), nil
}


func helmTemplate(ctx *ankh.ExecutionContext, charts []ankh.Chart, namespace string) (string, error) {
	finalOutput := ""
	if len(charts) > 0 {
		for _, chart := range charts {
			extraString := ""
			if chart.Version != "" {
				extraString = fmt.Sprintf(" at version \"%v\"", chart.Version)
			} else if chart.Path != "" {
				extraString = fmt.Sprintf(" from path \"%v\"", chart.Path)
			}
			ctx.Logger.Infof("Templating chart \"%s\"%s", chart.Name, extraString)
			chartOutput, err := templateChart(ctx, chart, namespace)
			if err != nil {
				return finalOutput, err
			}
			finalOutput += chartOutput
		}
		if namespace != "" {
			ctx.Logger.Infof("Finished templating charts for namespace %v", namespace)
		} else {
			ctx.Logger.Info("Finished templating charts with an explicit empty namespace")
		}
	} else {
		ctx.Logger.Info("Does not contain any charts. Nothing to do.")
	}
	return finalOutput, nil
}

