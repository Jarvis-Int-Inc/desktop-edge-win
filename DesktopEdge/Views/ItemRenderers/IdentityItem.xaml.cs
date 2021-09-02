﻿using System;
using System.Collections.Generic;
using System.Linq;
using System.Text;
using System.Threading.Tasks;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Data;
using System.Windows.Documents;
using System.Windows.Input;
using System.Windows.Media;
using System.Windows.Media.Imaging;
using System.Windows.Navigation;
using System.Windows.Shapes;
using ZitiDesktopEdge.Models;
using ZitiDesktopEdge.ServiceClient;

namespace ZitiDesktopEdge {
	/// <summary>
	/// User Control to list Identities and give status
	/// </summary>
	public partial class IdentityItem:UserControl {

		public delegate void StatusChanged(bool attached);
		public event StatusChanged OnStatusChanged;
		public delegate void OnAuthenticate(ZitiIdentity identity);
		public event OnAuthenticate Authenticate;
		private System.Windows.Forms.Timer _timer;
		private System.Windows.Forms.Timer _timingTimer;
		private int countdown = -1;

		public ZitiIdentity _identity;
		public ZitiIdentity Identity {
			get {
				return _identity;
			}
			set {
				_identity = value;
				this.RefreshUI();
			}
		}

		public void RefreshUI () {
			ToggleSwitch.Enabled = _identity.IsEnabled;
			if (_identity.IsMFAEnabled) {
				if (_identity.MFAInfo.IsAuthenticated) {
					ServiceCountArea.Visibility = Visibility.Visible;
					MfaRequired.Visibility = Visibility.Collapsed;
					ServiceCountAreaLabel.Content = "services";
					MainArea.Opacity = 1.0;
					if (_identity.MaxTimeout>0) {
						if (_timer != null) _timer.Stop();
						_timer = new System.Windows.Forms.Timer();
						_timer.Interval = _identity.MaxTimeout*1000;
						_timer.Tick += TimerTicked;
						_timer.Start();
					}
					if (_identity.MinTimeout > 0) {
						if (_timingTimer != null) _timingTimer.Stop();
						countdown = _identity.MinTimeout;
						_timingTimer = new System.Windows.Forms.Timer();
						_timingTimer.Interval = 1000;
						_timingTimer.Tick += TimingTimerTick; ;
						_timingTimer.Start();
					}
				} else {
					ServiceCountArea.Visibility = Visibility.Collapsed;
					MfaRequired.Visibility = Visibility.Visible;
					ServiceCountAreaLabel.Content = "authorize";
					MainArea.Opacity = 0.6;
				}
			} else {
				ServiceCountArea.Visibility = Visibility.Visible;
				MfaRequired.Visibility = Visibility.Collapsed;
				ServiceCountAreaLabel.Content = "services";
				MainArea.Opacity = 1.0;
			}
			IdName.Content = _identity.Name;
			IdUrl.Content = _identity.ControllerUrl;
			if (_identity.IsMFAEnabled && !_identity.MFAInfo.IsAuthenticated) {
				ServiceCount.Content = "MFA";
			} else {
				ServiceCount.Content = _identity.Services.Count.ToString();
			}
			TimerCountdown.ToolTip = _identity.TimeoutMessage;
			if (TimerCountdown.ToolTip.ToString().Length == 0) TimerCountdown.ToolTip = "Some or all of the services have timed out.";
			TimerCountdown.Visibility = _identity.IsTimingOut ? Visibility.Visible : Visibility.Collapsed;
			if (ToggleSwitch.Enabled) {
				ToggleStatus.Content = "ENABLED";
			} else {
				ToggleStatus.Content = "DISABLED";
			}
		}

		private void TimingTimerTick(object sender, EventArgs e) {
			if (countdown>-1) {
				countdown--;
				if (countdown<1260) {
					_identity.IsTimingOut = true;
				}

					if (countdown > 0) {
						TimeSpan t = TimeSpan.FromSeconds(countdown);
						string answer = t.Seconds + " seconds";
						if (t.Days > 0) answer = t.Days + " days " + t.Hours + " hours " + t.Minutes + " minutes " + t.Seconds + " seconds";
						else {
							if (t.Hours > 0) answer = t.Hours + " hours " + t.Minutes + " minutes " + t.Seconds + " seconds";
							else {
								if (t.Minutes > 0) answer = t.Minutes + " minutes " + t.Seconds + " seconds";
							}
						}
						TimerCountdown.ToolTip = "Some or all of the services will be timing out in " + answer;
					} else {
						TimerCountdown.ToolTip = "Some or all of the services have timed out.";
					}
					TimerCountdown.Visibility = _identity.IsTimingOut ? Visibility.Visible : Visibility.Collapsed;
			} else {
				TimerCountdown.ToolTip = "Some or all of the services have timed out.";
			}
		}

		private void TimerTicked(object sender, EventArgs e) {
			_identity.MFAInfo.IsAuthenticated = false;
			RefreshUI();
			_timer.Stop();
		}

		public IdentityItem() {
			InitializeComponent();
			ToggleSwitch.OnToggled += ToggleIdentity;
		}

		async private void ToggleIdentity(bool on) {
			try {
				if (OnStatusChanged != null) {
					OnStatusChanged(on);
				}
				DataClient client = (DataClient)Application.Current.Properties["ServiceClient"];
				DataStructures.Identity id = await client.IdentityOnOffAsync(_identity.Fingerprint, on);
				this.Identity.IsEnabled = on;
				if (on) {
					ToggleStatus.Content = "ENABLED";
				} else {
					ToggleStatus.Content = "DISABLED";
				}
			} catch (DataStructures.ServiceException se) {
				MessageBox.Show(se.AdditionalInfo, se.Message);
			} catch (Exception ex) {
				MessageBox.Show("Error", ex.Message);
			}
		}

		private void Canvas_MouseEnter(object sender, MouseEventArgs e) {
			OverState.Opacity = 0.2;
		}

		private void Canvas_MouseLeave(object sender, MouseEventArgs e) {
			OverState.Opacity = 0;
		}

		private void OpenDetails(object sender, MouseButtonEventArgs e) {
			IdentityDetails deets = ((MainWindow)Application.Current.MainWindow).IdentityMenu;
			deets.SelectedIdentity = this;
			deets.Identity = this.Identity;
		}

		private void MFAAuthenticate(object sender, MouseButtonEventArgs e) {
			this.Authenticate?.Invoke(_identity);
		}

		private void ToggledSwitch(object sender, MouseButtonEventArgs e) {
			ToggleSwitch.Toggle();
		}

		private void DoMFAOrOpen(object sender, MouseButtonEventArgs e) {
			if (MfaRequired.Visibility==Visibility.Visible) {
				MFAAuthenticate(sender, e);
			} else {
				OpenDetails(sender, e);
			}
		}
	}
}
