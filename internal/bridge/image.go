package bridge

import (
	"context"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	dockerclient "github.com/moby/moby/client"
)

func createContainerWithAutoPull(ctx context.Context, api DockerAPI, options dockerclient.ContainerCreateOptions, logger Logger, logPrefix string) (dockerclient.ContainerCreateResult, bool, error) {
	created, err := api.ContainerCreate(ctx, options)
	if err == nil || !cerrdefs.IsNotFound(err) {
		return created, false, err
	}

	image := options.Config.Image
	logger.Printf("%s_image_missing image=%q error=%q", logPrefix, image, err)
	pulled, pullErr := api.ImagePull(ctx, image, dockerclient.ImagePullOptions{})
	if pullErr != nil {
		logger.Printf("%s_image_pull_error image=%q error=%q", logPrefix, image, pullErr)
		return dockerclient.ContainerCreateResult{}, false, helperImagePullError(image, pullErr)
	}
	if waitErr := pulled.Wait(ctx); waitErr != nil {
		_ = pulled.Close()
		logger.Printf("%s_image_pull_wait_error image=%q error=%q", logPrefix, image, waitErr)
		return dockerclient.ContainerCreateResult{}, false, helperImagePullError(image, waitErr)
	}
	if closeErr := pulled.Close(); closeErr != nil {
		logger.Printf("%s_image_pull_close_error image=%q error=%q", logPrefix, image, closeErr)
		return dockerclient.ContainerCreateResult{}, false, helperImagePullError(image, closeErr)
	}
	logger.Printf("%s_image_pulled image=%q", logPrefix, image)

	created, err = api.ContainerCreate(ctx, options)
	return created, true, err
}

func helperImagePullError(helperImage string, err error) error {
	return withHints(
		fmt.Errorf("拉取 helper 镜像 %q 失败: %w", helperImage, err),
		restoreDockerAccessHint(),
		fmt.Sprintf("确认 Docker daemon 可以访问镜像仓库，或者用 `docker pull %s` 在宿主机上预先拉取", shellArg(helperImage)),
	)
}
