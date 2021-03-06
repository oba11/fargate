package ecs

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	awsecs "github.com/aws/aws-sdk-go/service/ecs"
	"github.com/jpignata/fargate/console"
)

const (
	detailNetworkInterfaceId  = "networkInterfaceId"
	detailSubnetId            = "subnetId"
	startedByFormat           = "fargate:%s"
	taskGroupStartedByPattern = "fargate:(.*)"
)

type Task struct {
	Cpu              string
	CreatedAt        time.Time
	DeploymentId     string
	DesiredStatus    string
	EniId            string
	EnvVars          []EnvVar
	Image            string
	LastStatus       string
	Memory           string
	SecurityGroupIds []string
	StartedBy        string
	SubnetId         string
	Command          []string
	TaskId           string
	TaskRole         string
}

func (t *Task) RunningFor() time.Duration {
	return time.Now().Sub(t.CreatedAt).Truncate(time.Second)
}

type TaskGroup struct {
	TaskGroupName string
	Instances     int64
}

type RunTaskInput struct {
	ClusterName       string
	Count             int64
	Command           []string
	EnvVars           []EnvVar
	SecurityGroupIds  []string
	SubnetIds         []string
	TaskDefinitionArn string
	TaskName          string
}

func (ecs *ECS) RunTask(i *RunTaskInput) {
	runTaskInput := &awsecs.RunTaskInput{
		Cluster:        aws.String(i.ClusterName),
		Count:          aws.Int64(i.Count),
		TaskDefinition: aws.String(i.TaskDefinitionArn),
		LaunchType:     aws.String(awsecs.CompatibilityFargate),
		StartedBy:      aws.String(fmt.Sprintf(startedByFormat, i.TaskName)),
		NetworkConfiguration: &awsecs.NetworkConfiguration{
			AwsvpcConfiguration: &awsecs.AwsVpcConfiguration{
				AssignPublicIp: aws.String(awsecs.AssignPublicIpEnabled),
				Subnets:        aws.StringSlice(i.SubnetIds),
				SecurityGroups: aws.StringSlice(i.SecurityGroupIds),
			},
		},
		Overrides: &awsecs.TaskOverride{
			ContainerOverrides: []*awsecs.ContainerOverride{},
		},
	}

	var environment []*awsecs.KeyValuePair
	for _, envVar := range i.EnvVars {
		environment = append(environment,
			&awsecs.KeyValuePair{
				Name:  aws.String(envVar.Key),
				Value: aws.String(envVar.Value),
			},
		)
	}

	if (len(i.Command) > 0) || (len(i.EnvVars) > 0) {
		runTaskInput.Overrides.ContainerOverrides = append(
			runTaskInput.Overrides.ContainerOverrides,
			&awsecs.ContainerOverride{
				Command: aws.StringSlice(i.Command),
				Environment: environment,
				Name:    aws.String(i.TaskName),
			},
		)
	}

	_, err := ecs.svc.RunTask(runTaskInput)

	if err != nil {
		console.ErrorExit(err, "Could not run ECS task")
	}
}

func (ecs *ECS) DescribeTasksForService(serviceName string) []Task {
	return ecs.listTasks(
		&awsecs.ListTasksInput{
			Cluster:     aws.String(ecs.ClusterName),
			LaunchType:  aws.String(awsecs.CompatibilityFargate),
			ServiceName: aws.String(serviceName),
		},
	)
}

func (ecs *ECS) DescribeTasksForTaskGroup(taskGroupName string) []Task {
	return ecs.listTasks(
		&awsecs.ListTasksInput{
			StartedBy: aws.String(fmt.Sprintf(startedByFormat, taskGroupName)),
			Cluster:   aws.String(ecs.ClusterName),
		},
	)
}

func (ecs *ECS) ListTaskGroups() []*TaskGroup {
	var taskGroups []*TaskGroup

	taskGroupStartedByRegexp := regexp.MustCompile(taskGroupStartedByPattern)

	input := &awsecs.ListTasksInput{
		Cluster: aws.String(ecs.ClusterName),
	}

OUTER:
	for _, task := range ecs.listTasks(input) {
		matches := taskGroupStartedByRegexp.FindStringSubmatch(task.StartedBy)

		if len(matches) == 2 {
			taskGroupName := matches[1]

			for _, taskGroup := range taskGroups {
				if taskGroup.TaskGroupName == taskGroupName {
					taskGroup.Instances++
					continue OUTER
				}
			}

			taskGroups = append(
				taskGroups,
				&TaskGroup{
					TaskGroupName: taskGroupName,
					Instances:     1,
				},
			)
		}
	}

	return taskGroups
}

func (ecs *ECS) StopTasks(taskIds []string) {
	for _, taskId := range taskIds {
		ecs.StopTask(taskId)
	}
}

func (ecs *ECS) StopTask(taskId string) {
	_, err := ecs.svc.StopTask(
		&awsecs.StopTaskInput{
			Cluster: aws.String(ecs.ClusterName),
			Task:    aws.String(taskId),
		},
	)

	if err != nil {
		console.ErrorExit(err, "Could not stop ECS task")
	}
}

func (ecs *ECS) listTasks(input *awsecs.ListTasksInput) []Task {
	var tasks []Task
	var taskArnBatches [][]string

	err := ecs.svc.ListTasksPages(
		input,
		func(resp *awsecs.ListTasksOutput, lastPage bool) bool {
			if len(resp.TaskArns) > 0 {
				taskArnBatches = append(taskArnBatches, aws.StringValueSlice(resp.TaskArns))
			}

			return true
		},
	)

	if err != nil {
		console.ErrorExit(err, "Could not list ECS tasks")
	}

	if len(taskArnBatches) > 0 {
		for _, taskArnBatch := range taskArnBatches {
			for _, task := range ecs.DescribeTasks(taskArnBatch) {
				tasks = append(tasks, task)
			}
		}
	}

	return tasks
}

func (ecs *ECS) DescribeTasks(taskIds []string) []Task {
	var tasks []Task

	if len(taskIds) == 0 {
		return tasks
	}

	resp, err := ecs.svc.DescribeTasks(
		&awsecs.DescribeTasksInput{
			Cluster: aws.String(ecs.ClusterName),
			Tasks:   aws.StringSlice(taskIds),
		},
	)

	if err != nil {
		console.ErrorExit(err, "Could not describe ECS tasks")
	}

	for _, t := range resp.Tasks {
		taskArn := aws.StringValue(t.TaskArn)
		contents := strings.Split(taskArn, "/")
		taskId := contents[len(contents)-1]

		task := Task{
			Cpu:           aws.StringValue(t.Cpu),
			CreatedAt:     aws.TimeValue(t.CreatedAt),
			DeploymentId:  ecs.getDeploymentId(aws.StringValue(t.TaskDefinitionArn)),
			DesiredStatus: aws.StringValue(t.DesiredStatus),
			LastStatus:    aws.StringValue(t.LastStatus),
			Memory:        aws.StringValue(t.Memory),
			TaskId:        taskId,
			StartedBy:     aws.StringValue(t.StartedBy),
		}

		taskDefinition := ecs.DescribeTaskDefinition(aws.StringValue(t.TaskDefinitionArn))
		task.Image = aws.StringValue(taskDefinition.ContainerDefinitions[0].Image)
		task.TaskRole = aws.StringValue(taskDefinition.TaskRoleArn)

		var keys []string
		if len(t.Overrides.ContainerOverrides[0].Environment) > 0 {
			for _, envOverride := range t.Overrides.ContainerOverrides[0].Environment {
				keys = append(keys, aws.StringValue(envOverride.Name))
				task.EnvVars = append(
					task.EnvVars,
					EnvVar{
						Key:   aws.StringValue(envOverride.Name),
						Value: aws.StringValue(envOverride.Value),
					},
				)
			}
		}

		for _, environment := range taskDefinition.ContainerDefinitions[0].Environment {
			for _, key := range keys {
				if aws.StringValue(environment.Name) == key {
					continue
				}

				task.EnvVars = append(
					task.EnvVars,
					EnvVar{
						Key:   aws.StringValue(environment.Name),
						Value: aws.StringValue(environment.Value),
					},
				)
			}
		}

		if len(t.Overrides.ContainerOverrides[0].Command) > 0 {
			task.Command = aws.StringValueSlice(t.Overrides.ContainerOverrides[0].Command)
		}

		if len(t.Attachments) == 1 {
			for _, detail := range t.Attachments[0].Details {
				switch aws.StringValue(detail.Name) {
				case detailNetworkInterfaceId:
					task.EniId = aws.StringValue(detail.Value)
				case detailSubnetId:
					task.SubnetId = aws.StringValue(detail.Value)
				}
			}
		}

		tasks = append(tasks, task)
	}

	return tasks
}
