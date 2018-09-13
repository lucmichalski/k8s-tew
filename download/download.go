package download

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"net/http"
	"os"
	"path"

	"github.com/darxkies/k8s-tew/config"
	"github.com/darxkies/k8s-tew/utils"
)

type CompressedFile struct {
	SourceFile string
	TargetFile string
}

type Downloader struct {
	config          *config.InternalConfig
	downloaderSteps utils.Tasks
	forceDownload   bool
	parallel        bool
}

func NewDownloader(config *config.InternalConfig, forceDownload bool, parallel bool) Downloader {
	downloader := Downloader{config: config, forceDownload: forceDownload, parallel: parallel}

	downloader.downloaderSteps = utils.Tasks{}
	downloader.addTask(downloader.copyK8STEW)
	downloader.addTask(downloader.downloadEtcdBinaries)
	downloader.addTask(downloader.downloadKubectl)
	downloader.addTask(downloader.downloadKubeApiServer)
	downloader.addTask(downloader.downloadKubeControllerManager)
	downloader.addTask(downloader.downloadKubeScheduler)
	downloader.addTask(downloader.downloadKubeProxy)
	downloader.addTask(downloader.downloadKubelet)
	downloader.addTask(downloader.downloadHelmBinary)
	downloader.addTask(downloader.downloadContainerdBinaries)
	downloader.addTask(downloader.downloadRuncBinary)
	downloader.addTask(downloader.downloadCriCtlBinary)
	downloader.addTask(downloader.downloadGobetweenBinary)
	downloader.addTask(downloader.downloadArkBinaries)

	return downloader
}

func (downloader *Downloader) addTask(task utils.Task) {
	downloader.downloaderSteps = append(downloader.downloaderSteps, func() error {
		defer utils.IncreaseProgressStep()

		return task()
	})
}

func (downloader Downloader) Steps() int {
	return len(downloader.downloaderSteps)
}

func (downloader Downloader) getURL(url, filename string) (string, error) {
	data := struct {
		Filename string
		Versions config.Versions
	}{
		Filename: filename,
		Versions: downloader.config.Config.Versions,
	}

	return utils.ApplyTemplate(url, url, data, false)
}

func (downloader Downloader) downloadFile(url, filename string) (bool, error) {
	if !downloader.forceDownload && utils.FileExists(filename) {
		utils.LogURL("Skipped downloading", url)

		return false, nil
	}

	utils.LogURL("Downloading", url)

	output, error := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
	if error != nil {
		return false, error
	}

	defer output.Close()

	response, error := http.Get(url)
	if error != nil {
		return false, error
	}

	defer response.Body.Close()

	_, error = io.Copy(output, response.Body)

	return true, error
}

func (downloader Downloader) downloadExecutable(urlTemplate, remoteFilename, filename string) error {
	url, error := downloader.getURL(urlTemplate, remoteFilename)
	if error != nil {
		return error
	}

	installed, error := downloader.downloadFile(url, filename)
	if error != nil {
		return error
	}

	if error := os.Chmod(filename, 0777); error != nil {
		return error
	}

	if installed {
		utils.LogFilename("Installed", filename)
	} else {
		utils.LogFilename("Skipped installing", filename)
	}

	return nil
}

func (downloader Downloader) extractTGZ(filename string, targetDirectory string) error {
	if error := utils.CreateDirectoryIfMissing(targetDirectory); error != nil {
		return error
	}

	file, error := os.Open(filename)

	if error != nil {
		return error
	}

	defer file.Close()

	gzipReader, error := gzip.NewReader(file)

	if error != nil {
		return error
	}

	tarReader := tar.NewReader(gzipReader)

	for true {
		header, error := tarReader.Next()

		if error == io.EOF {
			break
		}

		if error != nil {
			return error
		}

		fullName := path.Join(targetDirectory, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if error := utils.CreateDirectoryIfMissing(fullName); error != nil {
				return error
			}

		case tar.TypeReg:
			outputFile, error := os.OpenFile(fullName, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0555)

			if error != nil {
				return error
			}

			defer outputFile.Close()

			if _, error := io.Copy(outputFile, tarReader); error != nil {
				return error
			}
		default:
		}
	}

	return nil
}

func (downloader Downloader) downloadAndExtractTGZFiles(urlTemplate, baseName string, files []CompressedFile) error {
	// Check if files already exist
	exist := true
	temporaryDirectory := downloader.config.GetFullLocalAssetDirectory(utils.TEMPORARY_DIRECTORY)

	if downloader.forceDownload {
		exist = false

	} else {
		for _, compressedFile := range files {
			if !utils.FileExists(compressedFile.TargetFile) {
				exist = false

				break
			}
		}
	}

	// Build base name including the version number
	baseName, error := downloader.getURL(baseName, "")
	if error != nil {
		return error
	}

	url, error := downloader.getURL(urlTemplate, baseName)
	if error != nil {
		return error
	}

	// All files exist, print skip message and bail out
	if exist {
		utils.LogURL("Skipped downloading", url)

		for _, compressedFile := range files {
			utils.LogFilename("Skipped installing", compressedFile.TargetFile)
		}

		return nil
	}

	// Create temporary download filename
	temporaryFile := path.Join(temporaryDirectory, baseName+".tgz")

	// Download file
	_, error = downloader.downloadFile(url, temporaryFile)
	if error != nil {
		return error
	}

	// Make sure the file is deleted once done
	defer func() {
		_ = os.Remove(temporaryFile)
	}()

	// Create temporary directory to extract to
	temporaryExtractedDirectory := path.Join(temporaryDirectory, baseName)

	// Extrace files
	if error := downloader.extractTGZ(temporaryFile, temporaryExtractedDirectory); error != nil {
		return error
	}

	// Make sure the temporary directory is removed once done
	defer func() {
		_ = os.RemoveAll(temporaryExtractedDirectory)
	}()

	// Move files from temporary directory to target directory
	for _, compressedFile := range files {
		if error := os.Rename(path.Join(temporaryExtractedDirectory, compressedFile.SourceFile), compressedFile.TargetFile); error != nil {
			return error
		}

		utils.LogFilename("Installed", compressedFile.TargetFile)
	}

	return nil
}

func (downloader Downloader) copyK8STEW() error {
	binaryName, error := os.Executable()

	if error != nil {
		return error
	}

	targetFilename := downloader.config.GetFullLocalAssetFilename(utils.K8S_TEW_BINARY)

	if binaryName == targetFilename {
		utils.LogFilename("Skipped", targetFilename)

		return nil
	}

	sourceFile, error := os.Open(binaryName)

	if error != nil {
		return error
	}

	defer sourceFile.Close()

	targetFile, error := os.OpenFile(targetFilename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)

	if error != nil {
		return error
	}

	defer targetFile.Close()

	_, error = io.Copy(targetFile, sourceFile)

	if error != nil {
		return error
	}

	utils.LogFilename("Copied", targetFilename)

	return targetFile.Sync()
}

func (downloader Downloader) downloadKubectl() error {
	return downloader.downloadExecutable(utils.K8S_DOWNLOAD_URL, utils.KUBECTL_BINARY, downloader.config.GetFullLocalAssetFilename(utils.KUBECTL_BINARY))
}

func (downloader Downloader) downloadKubeApiServer() error {
	return downloader.downloadExecutable(utils.K8S_DOWNLOAD_URL, utils.KUBE_APISERVER_BINARY, downloader.config.GetFullLocalAssetFilename(utils.KUBE_APISERVER_BINARY))
}

func (downloader Downloader) downloadKubeControllerManager() error {
	return downloader.downloadExecutable(utils.K8S_DOWNLOAD_URL, utils.KUBE_CONTROLLER_MANAGER_BINARY, downloader.config.GetFullLocalAssetFilename(utils.KUBE_CONTROLLER_MANAGER_BINARY))
}

func (downloader Downloader) downloadKubeScheduler() error {
	return downloader.downloadExecutable(utils.K8S_DOWNLOAD_URL, utils.KUBE_SCHEDULER_BINARY, downloader.config.GetFullLocalAssetFilename(utils.KUBE_SCHEDULER_BINARY))
}

func (downloader Downloader) downloadKubeProxy() error {
	return downloader.downloadExecutable(utils.K8S_DOWNLOAD_URL, utils.KUBE_PROXY_BINARY, downloader.config.GetFullLocalAssetFilename(utils.KUBE_PROXY_BINARY))
}

func (downloader Downloader) downloadKubelet() error {
	return downloader.downloadExecutable(utils.K8S_DOWNLOAD_URL, utils.KUBELET_BINARY, downloader.config.GetFullLocalAssetFilename(utils.KUBELET_BINARY))
}

func (downloader Downloader) downloadHelmBinary() error {
	compressedFiles := []CompressedFile{
		CompressedFile{
			SourceFile: path.Join("linux-amd64", utils.HELM_BINARY),
			TargetFile: downloader.config.GetFullLocalAssetFilename(utils.HELM_BINARY),
		},
	}

	return downloader.downloadAndExtractTGZFiles(utils.HELM_DOWNLOAD_URL, utils.HELM_BASE_NAME, compressedFiles)
}

func (downloader Downloader) downloadRuncBinary() error {
	return downloader.downloadExecutable(utils.RUNC_DOWNLOAD_URL, "", downloader.config.GetFullLocalAssetFilename(utils.RUNC_BINARY))
}

func (downloader Downloader) downloadEtcdBinaries() error {
	// Build base name including the version number
	baseName, error := downloader.getURL(utils.ETCD_BASE_NAME, "")
	if error != nil {
		return error
	}

	compressedFiles := []CompressedFile{
		CompressedFile{
			SourceFile: path.Join(baseName, utils.ETCD_BINARY),
			TargetFile: downloader.config.GetFullLocalAssetFilename(utils.ETCD_BINARY),
		},
		CompressedFile{
			SourceFile: path.Join(baseName, utils.ETCDCTL_BINARY),
			TargetFile: downloader.config.GetFullLocalAssetFilename(utils.ETCDCTL_BINARY),
		},
	}

	return downloader.downloadAndExtractTGZFiles(utils.ETCD_DOWNLOAD_URL, utils.ETCD_BASE_NAME, compressedFiles)
}

func (downloader Downloader) downloadContainerdBinaries() error {
	compressedFiles := []CompressedFile{
		CompressedFile{
			SourceFile: path.Join("bin", utils.CONTAINERD_BINARY),
			TargetFile: downloader.config.GetFullLocalAssetFilename(utils.CONTAINERD_BINARY),
		},
		CompressedFile{
			SourceFile: path.Join("bin", utils.CONTAINERD_SHIM_BINARY),
			TargetFile: downloader.config.GetFullLocalAssetFilename(utils.CONTAINERD_SHIM_BINARY),
		},
		CompressedFile{
			SourceFile: path.Join("bin", utils.CTR_BINARY),
			TargetFile: downloader.config.GetFullLocalAssetFilename(utils.CTR_BINARY),
		},
	}

	return downloader.downloadAndExtractTGZFiles(utils.CONTAINERD_DOWNLOAD_URL, utils.CONTAINERD_BASE_NAME, compressedFiles)
}

func (downloader Downloader) downloadCriCtlBinary() error {
	compressedFiles := []CompressedFile{
		CompressedFile{
			SourceFile: utils.CRICTL_BINARY,
			TargetFile: downloader.config.GetFullLocalAssetFilename(utils.CRICTL_BINARY),
		},
	}

	return downloader.downloadAndExtractTGZFiles(utils.CRICTL_DOWNLOAD_URL, utils.CRICTL_BASE_NAME, compressedFiles)
}

func (downloader Downloader) downloadGobetweenBinary() error {
	compressedFiles := []CompressedFile{
		CompressedFile{
			SourceFile: utils.GOBETWEEN_BINARY,
			TargetFile: downloader.config.GetFullLocalAssetFilename(utils.GOBETWEEN_BINARY),
		},
	}

	return downloader.downloadAndExtractTGZFiles(utils.GOBETWEEN_DOWNLOAD_URL, utils.GOBETWEEN_BASE_NAME, compressedFiles)
}

func (downloader Downloader) downloadArkBinaries() error {
	compressedFiles := []CompressedFile{
		CompressedFile{
			SourceFile: utils.ARK_BINARY,
			TargetFile: downloader.config.GetFullLocalAssetFilename(utils.ARK_BINARY),
		},
		CompressedFile{
			SourceFile: utils.ARK_RESTIC_RESTORE_HELPER_BINARY,
			TargetFile: downloader.config.GetFullLocalAssetFilename(utils.ARK_RESTIC_RESTORE_HELPER_BINARY),
		},
	}

	return downloader.downloadAndExtractTGZFiles(utils.ARK_DOWNLOAD_URL, utils.ARK_BASE_NAME, compressedFiles)
}

func (downloader Downloader) createLocalDirectories() error {
	// Remove any temporary remains of previous downloads
	os.RemoveAll(downloader.config.GetFullLocalAssetDirectory(utils.TEMPORARY_DIRECTORY))

	for name, directory := range downloader.config.Config.Assets.Directories {
		if directory.Absolute {
			continue
		}

		localDirectory := downloader.config.GetFullLocalAssetDirectory(name)

		if error := utils.CreateDirectoryIfMissing(localDirectory); error != nil {
			return error
		}
	}

	return nil
}

func (downloader Downloader) DownloadBinaries() error {
	if error := downloader.createLocalDirectories(); error != nil {
		return error
	}

	errors := utils.RunParallelTasks(downloader.downloaderSteps, downloader.parallel)
	if len(errors) > 0 {
		return errors[0]
	}

	return nil
}
