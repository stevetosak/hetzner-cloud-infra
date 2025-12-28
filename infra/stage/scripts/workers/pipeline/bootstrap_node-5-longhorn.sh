echo "Disabling multipath..."
sudo systemctl stop multipathd
sudo systemctl disable multipathd
sudo systemctl mask multipathd
sudo systemctl stop multipathd.socket
echo "Done"



